package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

// The row files are consumed by the audit gate, which weights a candidate by
// how far to trust the map. A limitation that suppresses findings has to travel
// with the rows, not only with the graph they were derived from.
func TestDeadViewCarriesGraphLimitations(t *testing.T) {
	g := Graph{
		Computable:  true,
		Fidelity:    "semantic",
		Limitations: Limitations{{ID: "rust-derive-over-rooting", Effect: EffectOverApproximatesLive}},
		Nodes:       []Node{{ID: 1, Root: true}, {ID: 2}},
	}
	n := g.DeadView(Meta{Generator: "magma/0.3.0"})
	if len(n.Limitations) != 1 || n.Limitations[0].ID != "rust-derive-over-rooting" {
		t.Fatalf("DeadView dropped limitations: %+v", n.Limitations)
	}
}

func TestTestOnlyViewCarriesGraphLimitations(t *testing.T) {
	g := Graph{
		Computable:  true,
		Limitations: Limitations{{ID: "go-closure-edges", Effect: EffectMayOmitEdges}},
		Nodes:       []Node{{ID: 1, Root: true}, {ID: 2}},
	}
	n := g.TestOnlyView(Meta{})
	if len(n.Limitations) != 1 || n.Limitations[0].ID != "go-closure-edges" {
		t.Fatalf("TestOnlyView dropped limitations: %+v", n.Limitations)
	}
}

// A refused note needs them most of all.
func TestRefusedNoteCarriesLimitations(t *testing.T) {
	g := Graph{Computable: true, Limitations: Limitations{{ID: "x"}}}
	n := g.Refuse("no entrypoint").TestOnlyView(Meta{})
	if n.ReachabilityComputable {
		t.Fatal("refused graph must yield a refused note")
	}
	if len(n.Limitations) != 1 {
		t.Fatalf("refused note dropped limitations: %+v", n.Limitations)
	}
}

// Never null on the wire, for the same reason as on the graph.
func TestNoteLimitationsMarshalAsArrayWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Meta{}.Computed(nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// json.Marshal, not MarshalIndent, so there is no space after the colon.
	// The claim under test is `[]` rather than `null`, not the whitespace.
	if !strings.Contains(string(b), `"limitations":[]`) {
		t.Fatalf("empty limitations must marshal as [], got: %s", b)
	}
}
