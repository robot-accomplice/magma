package backend

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

// find returns the limitation with the given id, or nil.
func find(ls contract.Limitations, id string) *contract.Limitation {
	for i := range ls {
		if ls[i].ID == id {
			return &ls[i]
		}
	}
	return nil
}

// The Go backend's known limitation is documented in README prose today and is
// invisible to any machine consumer. It must ride on the artifact.
func TestGoBackendDeclaresClosureEdgeLimitation(t *testing.T) {
	g := buildFixture(t, "livemod")
	l := find(g.Limitations, "go-closure-edges")
	if l == nil {
		t.Fatalf("go backend declared no go-closure-edges limitation: %+v", g.Limitations)
	}
	if l.Scope != contract.ScopeBackend {
		t.Errorf("scope = %q, want %q", l.Scope, contract.ScopeBackend)
	}
	if l.Effect != contract.EffectMayOmitEdges {
		t.Errorf("effect = %q, want %q", l.Effect, contract.EffectMayOmitEdges)
	}
	if l.Description == "" || l.Attribution == "" {
		t.Error("description and attribution must be written, not left empty")
	}
}

// Both Rust limitations are MEASURED facts, and the first is why this whole
// field exists: at 87% roots a consumer's UI showed "no dead code".
func TestRustBackendDeclaresKnownLimitations(t *testing.T) {
	ls := rustLimitations()
	want := map[string]string{
		"rust-derive-over-rooting":       contract.EffectOverApproximatesLive,
		"rust-format-args-concrete-only": contract.EffectMayOmitEdges,
	}
	for id, effect := range want {
		l := find(ls, id)
		if l == nil {
			t.Errorf("rust backend declared no %q limitation", id)
			continue
		}
		if l.Effect != effect {
			t.Errorf("limitation %q effect = %q, want %q", id, l.Effect, effect)
		}
		if l.Attribution == "" || l.Description == "" {
			t.Errorf("limitation %q has an empty attribution or description", id)
		}
	}
}

// The over-rooting limitation must point at the count that quantifies it,
// otherwise a consumer cannot tell HOW MUCH it bit on this run.
func TestRustOverRootingIsEvidencedByRootRatio(t *testing.T) {
	l := find(rustLimitations(), "rust-derive-over-rooting")
	if l == nil {
		t.Fatal("missing rust-derive-over-rooting")
	}
	if l.EvidencedBy != "root_ratio" {
		t.Errorf("EvidencedBy = %q, want root_ratio", l.EvidencedBy)
	}
	if l.Scope != contract.ScopeAnalyzer {
		t.Errorf("scope = %q, want %q — it is rust-analyzer's visibility rule, not magma's code", l.Scope, contract.ScopeAnalyzer)
	}
}

// Limitations must survive a refusal, on both backends. A refusal is when a
// consumer most needs them, and both backends refuse AFTER NewGraph.
func TestLimitationsSurviveRefusal(t *testing.T) {
	g := contract.NewGraph(contract.Meta{}, "rust", "semantic")
	g.Limitations = rustLimitations()
	refused := g.Refuse("no entrypoint")
	if len(refused.Limitations) != len(rustLimitations()) {
		t.Fatalf("refusal dropped limitations: %+v", refused.Limitations)
	}
}
