package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var testMeta = Meta{Generator: "magma/test", SHA: "abc123", Tree: "abc123", Fidelity: "rta"}

// Computed sorts rows by (file, line, symbol) so output is stable and diffable.
func TestComputedSortsRows(t *testing.T) {
	rows := []Row{
		{Symbol: "Z", File: "b.go", Line: 1},
		{Symbol: "A", File: "a.go", Line: 9},
		{Symbol: "B", File: "a.go", Line: 2},
		{Symbol: "Y", File: "a.go", Line: 2}, // same file+line as B, tiebreak on symbol
	}
	got := testMeta.Computed(rows).Rows
	want := []Row{
		{Symbol: "B", File: "a.go", Line: 2},
		{Symbol: "Y", File: "a.go", Line: 2},
		{Symbol: "A", File: "a.go", Line: 9},
		{Symbol: "Z", File: "b.go", Line: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A computed note stamps provenance, marks reachability computable, and — the
// load-bearing distinction — turns a nil row slice into [] (found nothing), not
// null (could not compute).
func TestComputedFieldsAndEmptyIsNotNull(t *testing.T) {
	n := testMeta.Computed(nil)
	if n.ContractVersion != Version {
		t.Errorf("ContractVersion = %q, want %q", n.ContractVersion, Version)
	}
	if n.Generator != "magma/test" || n.SHA != "abc123" || n.Tree != "abc123" || n.Fidelity != "rta" {
		t.Errorf("provenance not carried through: %+v", n)
	}
	if !n.ReachabilityComputable {
		t.Error("computed note must have ReachabilityComputable=true")
	}
	if n.Rows == nil {
		t.Error("computed note with no rows must have [] rows, got nil (marshals to null)")
	}
	b, _ := json.Marshal(n)
	if got := string(b); !contains(got, `"rows":[]`) {
		t.Errorf("empty computed rows must marshal to [], got %s", got)
	}
}

// A refused note keeps rows nil (marshals to null) and carries the reason, so a
// consumer can never mistake a refusal for "found nothing".
func TestRefusedIsNullWithReason(t *testing.T) {
	n := testMeta.Refused("no production main")
	if n.ReachabilityComputable {
		t.Error("refused note must have ReachabilityComputable=false")
	}
	if n.NotComputableReason != "no production main" {
		t.Errorf("reason = %q, want %q", n.NotComputableReason, "no production main")
	}
	if n.Rows != nil {
		t.Errorf("refused note must keep Rows nil (marshals to null), got %+v", n.Rows)
	}
	b, _ := json.Marshal(n)
	if got := string(b); !contains(got, `"rows":null`) {
		t.Errorf("refused rows must marshal to null, got %s", got)
	}
}

// Write emits <dir>/<name>.json with a trailing newline and round-trips.
func TestWrite(t *testing.T) {
	dir := t.TempDir()
	in := testMeta.Computed([]Row{{Symbol: "F", File: "x.go", Line: 3}})
	if err := Write(dir, "_dead", in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "_dead.json"))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Error("output must end with a trailing newline")
	}
	var out Note
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Symbol != "F" {
		t.Errorf("round-trip lost rows: %+v", out.Rows)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
