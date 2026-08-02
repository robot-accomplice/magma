package contract

import "testing"

// Disclosure is derived from the finished graph, so no backend computes counts
// and no two backends can disagree about what "root ratio" means.
func TestFinalizeDerivesDisclosure(t *testing.T) {
	g := Graph{Computable: true, Nodes: []Node{
		{ID: 1, Root: true},
		{ID: 2, Root: true},
		{ID: 3, Generated: true},
		{ID: 4},
	}, Edges: []Edge{
		{From: 1, To: 2, Kind: "dynamic"},
		{From: 1, To: 4, Kind: "static"},
	}}
	got := g.Finalize()
	if got.Disclosure == nil {
		t.Fatal("Finalize did not attach a Disclosure")
	}
	d := got.Disclosure
	if d.Nodes != 4 || d.Roots != 2 || d.Generated != 1 || d.DynamicEdges != 1 {
		t.Errorf("counts = %+v", d)
	}
	if d.RootRatio != 0.5 {
		t.Errorf("RootRatio = %v, want 0.5", d.RootRatio)
	}
}

// A refused graph has nothing to measure, but MUST still declare limitations —
// a refusal is exactly when a consumer needs to know what the backend cannot do.
func TestRefusedGraphKeepsLimitationsAndOmitsDisclosure(t *testing.T) {
	g := Graph{Computable: true, Limitations: Limitations{{ID: "x", Scope: ScopeBackend}}}
	got := g.Refuse("no entrypoint").Finalize()
	if len(got.Limitations) != 1 {
		t.Errorf("Refuse dropped limitations: %+v", got.Limitations)
	}
	if got.Disclosure != nil {
		t.Errorf("refused graph should have no Disclosure, got %+v", got.Disclosure)
	}
}

// Zero nodes must not divide by zero.
func TestFinalizeEmptyGraphRootRatioZero(t *testing.T) {
	got := Graph{Computable: true}.Finalize()
	if got.Disclosure == nil {
		t.Fatal("Finalize did not attach a Disclosure")
	}
	if got.Disclosure.RootRatio != 0 {
		t.Errorf("RootRatio = %v, want 0", got.Disclosure.RootRatio)
	}
}

// The Bevy case, as a regression: at 87% roots the dead set collapses, and
// root_ratio is what makes that visible instead of silent.
func TestFinalizeRootRatioSurfacesOverRooting(t *testing.T) {
	nodes := make([]Node, 156)
	for i := range nodes {
		nodes[i] = Node{ID: i + 1, Root: i < 136}
	}
	d := Graph{Computable: true, Nodes: nodes}.Finalize().Disclosure
	if d.RootRatio != 0.872 {
		t.Errorf("RootRatio = %v, want 0.872 (136/156)", d.RootRatio)
	}
}
