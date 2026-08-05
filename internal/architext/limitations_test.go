package architext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

// Architext acked both fields on 2026-08-01 and will CONSUME limitations in
// their "How this map was made" affordance, so the emit must carry them.
func TestEmitCarriesLimitationsAndDisclosure(t *testing.T) {
	g := contract.Graph{
		Computable:  true,
		Limitations: contract.Limitations{{ID: "rust-derive-over-rooting", Effect: contract.EffectOverApproximatesLive}},
		Nodes:       []contract.Node{{ID: 1, Root: true}, {ID: 2}},
	}.Finalize()

	cg := Emit(g)
	if len(cg.Limitations) != 1 || cg.Limitations[0].ID != "rust-derive-over-rooting" {
		t.Fatalf("Emit dropped limitations: %+v", cg.Limitations)
	}
	if cg.Disclosure == nil {
		t.Fatal("Emit dropped disclosure")
	}
	if cg.Disclosure.RootRatio != 0.5 {
		t.Errorf("RootRatio = %v, want 0.5", cg.Disclosure.RootRatio)
	}
}

// Never null: a backend that looks unlimited is exactly the wrong lesson, and
// absence of the field would read as absence of limitations.
func TestEmitLimitationsMarshalAsArrayWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Emit(contract.Graph{Computable: true}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"limitations":[]`) {
		t.Fatalf("empty limitations must marshal as [], got: %s", b)
	}
}

// A refused graph carries limitations and omits disclosure, all the way out to
// the artifact Architext reads.
func TestEmitRefusedCarriesLimitationsWithoutDisclosure(t *testing.T) {
	g := contract.Graph{Computable: true, Limitations: contract.Limitations{{ID: "x"}}}.
		Refuse("no entrypoint").Finalize()
	cg := Emit(g)
	if len(cg.Limitations) != 1 {
		t.Errorf("refused emit dropped limitations: %+v", cg.Limitations)
	}
	if cg.Disclosure != nil {
		t.Errorf("refused emit should carry no disclosure, got %+v", cg.Disclosure)
	}
}
