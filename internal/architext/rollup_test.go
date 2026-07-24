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
		{From: 0, To: 2, Kind: "dynamic"}, // a -> b, dynamic
		{From: 2, To: 2, Kind: "static"},  // b -> b, intra-module (excluded from module_calls)
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
	if byID["a"].FanOut != 1 || byID["a"].FanIn != 0 {
		t.Errorf("module a fan = in%d out%d, want in0 out1", byID["a"].FanIn, byID["a"].FanOut)
	}
	if byID["b"].FanIn != 1 || byID["b"].FanOut != 0 {
		t.Errorf("module b fan = in%d out%d, want in1 out0", byID["b"].FanIn, byID["b"].FanOut)
	}
	if len(mcalls) != 1 {
		t.Fatalf("module_calls = %d, want 1 (a->b; intra-module b->b excluded)", len(mcalls))
	}
	if mcalls[0].From != "a" || mcalls[0].To != "b" || !mcalls[0].HasDynamic || mcalls[0].Count != 1 {
		t.Errorf("module_call = %+v, want a->b count=1 has_dynamic=true", mcalls[0])
	}
}
