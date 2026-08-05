package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

// A nil Limitations must marshal as [], not null. `null` claims "limitations
// unknown"; `[]` claims "none declared". Only the second is ever true of a
// backend magma ships, and the Signature bug proved an invariant living in a
// backend rather than in the type does not survive a second backend.
func TestLimitationsNeverMarshalNull(t *testing.T) {
	var l Limitations
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "[]" {
		t.Fatalf("nil Limitations marshalled as %s, want []", b)
	}
}

func TestLimitationRoundTripsAllFields(t *testing.T) {
	l := Limitations{{
		ID:          "go-closure-edges",
		Scope:       ScopeBackend,
		Attribution: "magma go backend",
		Description: "calls routed through closures or synthetic wrappers are not emitted as node edges",
		Effect:      EffectMayOmitEdges,
		EvidencedBy: "dynamic_edges",
	}}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"id":"go-closure-edges"`, `"scope":"backend"`,
		`"attribution":"magma go backend"`, `"effect":"may-omit-edges"`,
		`"evidenced_by":"dynamic_edges"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("marshalled limitation missing %s: %s", want, b)
		}
	}
}

// An unrecognised scope or effect must pass through untouched. A closed enum
// would reject the whole document — the failure this design exists to avoid —
// so there must be no validation that drops or rewrites a value.
func TestUnknownScopeAndEffectPassThrough(t *testing.T) {
	l := Limitations{{ID: "x", Scope: "run-configuration", Effect: "may-invent-nodes"}}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"scope":"run-configuration"`) ||
		!strings.Contains(string(b), `"effect":"may-invent-nodes"`) {
		t.Fatalf("unknown values were not preserved: %s", b)
	}
}

// EvidencedBy is optional; omitting it must not emit an empty key.
func TestEvidencedByOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Limitations{{ID: "x", Scope: ScopeLanguage}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "evidenced_by") {
		t.Fatalf("empty EvidencedBy should be omitted: %s", b)
	}
}
