package architext

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestRollupAggregates(t *testing.T) {
	nodes := []contract.Node{
		{ID: 0, Pkg: "a", Symbol: "Main", Root: true, Reachable: true, ProdReachable: true},
		{ID: 1, Pkg: "a", Symbol: "Dead"}, // unreachable, non-root -> dead
		{ID: 2, Pkg: "b", Symbol: "Helper", Reachable: true, ProdReachable: true},
	}
	edges := []contract.Edge{
		{From: 0, To: 2, Kind: "dynamic"}, // a -> b (Main -> Helper), dynamic
		{From: 1, To: 2, Kind: "static"},  // a -> b (Dead -> Helper): a SECOND underlying call for the SAME module pair
		{From: 2, To: 2, Kind: "static"},  // b -> b, intra-module (excluded from module_calls AND fan)
	}
	ids := assignIDs(nodes)
	mods, mcalls := rollup(nodes, edges, ids)

	byID := map[string]Module{}
	for _, m := range mods {
		byID[m.ID] = m
	}
	if byID["a"].Counts.Functions != 2 || byID["a"].Counts.Dead != 1 {
		t.Errorf("module a counts = %+v, want functions=2 dead=1", byID["a"].Counts)
	}
	// Module fan = distinct module-graph degree: a has one out-edge (a->b), no in;
	// b has one in-edge (from a), no out (its self-edge b->b is intra-module).
	// Two underlying a->b calls collapse into ONE module edge, so degree stays 1
	// despite 2 underlying calls — this is what distinguishes post-aggregation
	// degree counting from an (incorrect) per-edge-increment regression, which
	// would make FanOut/FanIn == 2 here.
	if byID["a"].FanOut != 1 || byID["a"].FanIn != 0 {
		t.Errorf("module a fan = in%d out%d, want in0 out1", byID["a"].FanIn, byID["a"].FanOut)
	}
	if byID["b"].FanIn != 1 || byID["b"].FanOut != 0 {
		t.Errorf("module b fan = in%d out%d, want in1 out0", byID["b"].FanIn, byID["b"].FanOut)
	}
	if len(mcalls) != 1 {
		t.Fatalf("module_calls = %d, want 1 (a->b; intra-module b->b excluded)", len(mcalls))
	}
	if mcalls[0].From != "a" || mcalls[0].To != "b" || !mcalls[0].HasDynamic || mcalls[0].Count != 2 {
		t.Errorf("module_call = %+v, want a->b count=2 has_dynamic=true", mcalls[0])
	}
}
