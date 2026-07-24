package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// exhaustiveGraph is one computable graph whose nodes cover every branch of the
// two view filters at once. It has a production main (so views compute rather
// than refuse), one live node, and one representative of each thing a view must
// KEEP or EXCLUDE.
func exhaustiveGraph() Graph {
	g := NewGraph(testMeta, "go", "rta")
	g.Nodes = []Node{
		// id0: production main — the prod root that lets views compute.
		{ID: 0, Symbol: "main", File: "main.go", Line: 1, Root: true, Reachable: true, ProdReachable: true},
		// id1: live everywhere — excluded from both views.
		{ID: 1, Symbol: "Live", File: "a.go", Line: 10, Reachable: true, ProdReachable: true},
		// id2: DEAD — reached by nothing, not generated, not a root.
		{ID: 2, Symbol: "Dead", File: "a.go", Line: 20},
		// id3: TEST-ONLY — reached, but only via tests; production code, not a test file.
		{ID: 3, Symbol: "OnlyTest", File: "a.go", Line: 30, Reachable: true, ProdReachable: false},
		// id4: dead but GENERATED — excluded from _dead.
		{ID: 4, Symbol: "GenDead", File: "z.gen.go", Line: 5, Generated: true},
		// id5: test-reached but declared IN a test file — excluded from _test-only.
		{ID: 5, Symbol: "InTestFile", File: "a_test.go", Line: 8, Reachable: true, Test: true},
		// id6: test-reached but GENERATED — excluded from _test-only.
		{ID: 6, Symbol: "GenTestOnly", File: "z.gen.go", Line: 40, Reachable: true, Generated: true},
	}
	return g
}

// _dead is exactly the source nodes reached by no root, excluding generated and
// root nodes.
func TestDeadViewKeepsOnlyUnreachableSource(t *testing.T) {
	got := symbols(exhaustiveGraph().DeadView(testMeta).Rows)
	assertSet(t, "_dead", got, []string{"Dead"})
}

// _test-only is exactly production functions reached only through tests,
// excluding test-file nodes, generated nodes, and roots.
func TestTestOnlyViewKeepsOnlyTestReachedProduction(t *testing.T) {
	got := symbols(exhaustiveGraph().TestOnlyView(testMeta).Rows)
	assertSet(t, "_test-only", got, []string{"OnlyTest"})
}

// A refused graph yields refused views carrying the SAME reason — the three
// artifacts can never disagree on computability.
func TestViewsMirrorRefusedGraph(t *testing.T) {
	g := NewGraph(testMeta, "go", "rta").Refuse("boom")
	for name, n := range map[string]Note{"_dead": g.DeadView(testMeta), "_test-only": g.TestOnlyView(testMeta)} {
		if n.ReachabilityComputable {
			t.Errorf("%s: refused graph must yield refused view", name)
		}
		if n.NotComputableReason != "boom" {
			t.Errorf("%s: reason = %q, want %q", name, n.NotComputableReason, "boom")
		}
		if n.Rows != nil {
			t.Errorf("%s: refused view must have nil rows, got %+v", name, n.Rows)
		}
	}
}

// A computable graph with no PRODUCTION main refuses both views with the
// no-prod-main reason: without an external-caller root, reachability would be
// almost all false positives, so answering would be lying.
func TestViewsRefuseWithoutProductionMain(t *testing.T) {
	g := NewGraph(testMeta, "go", "rta")
	g.Nodes = []Node{
		{ID: 0, Symbol: "main", File: "main_test.go", Line: 1, Root: true, Test: true}, // a TEST main is not a prod root
		{ID: 1, Symbol: "Dead", File: "a.go", Line: 2},
	}
	for name, n := range map[string]Note{"_dead": g.DeadView(testMeta), "_test-only": g.TestOnlyView(testMeta)} {
		if n.ReachabilityComputable {
			t.Errorf("%s: no-prod-main graph must refuse", name)
		}
		if n.NotComputableReason != noProdMain {
			t.Errorf("%s: reason = %q, want %q", name, n.NotComputableReason, noProdMain)
		}
	}
}

// Views take fidelity from the graph, not the caller's Meta — the note must
// describe what the edges actually mean.
func TestViewFidelityComesFromGraph(t *testing.T) {
	g := exhaustiveGraph()
	g.Fidelity = "syntactic"
	callerMeta := testMeta // Fidelity "rta"
	if got := g.DeadView(callerMeta).Fidelity; got != "syntactic" {
		t.Errorf("DeadView fidelity = %q, want the graph's %q", got, "syntactic")
	}
}

// NewGraph seeds a computable envelope; Refuse flips it and clears node/edge data.
func TestNewGraphAndRefuse(t *testing.T) {
	g := NewGraph(testMeta, "go", "rta")
	if !g.Computable || g.ContractVersion != GraphVersion || g.Language != "go" || g.Fidelity != "rta" {
		t.Errorf("NewGraph envelope wrong: %+v", g)
	}
	g.Nodes = []Node{{ID: 0}}
	g.Edges = []Edge{{From: 0, To: 0}}
	r := g.Refuse("nope")
	if r.Computable || r.NotComputableReason != "nope" || r.Nodes != nil || r.Edges != nil {
		t.Errorf("Refuse must clear nodes/edges and set reason: %+v", r)
	}
}

// WriteGraph emits graph.json with nodes id-sorted and edges (from,to)-sorted.
func TestWriteGraphSortsAndWrites(t *testing.T) {
	dir := t.TempDir()
	g := NewGraph(testMeta, "go", "rta")
	g.Nodes = []Node{{ID: 2, Symbol: "c"}, {ID: 0, Symbol: "a"}, {ID: 1, Symbol: "b"}}
	g.Edges = []Edge{{From: 1, To: 0}, {From: 0, To: 2}, {From: 0, To: 1}}
	if err := WriteGraph(dir, g); err != nil {
		t.Fatalf("WriteGraph: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "graph.json"))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if raw[len(raw)-1] != '\n' {
		t.Error("graph.json must end with a trailing newline")
	}
	var out Graph
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	for i, n := range out.Nodes {
		if n.ID != i {
			t.Errorf("nodes not id-sorted: node %d has ID %d", i, n.ID)
		}
	}
	want := []Edge{{From: 0, To: 1}, {From: 0, To: 2}, {From: 1, To: 0}}
	for i := range want {
		if out.Edges[i].From != want[i].From || out.Edges[i].To != want[i].To {
			t.Errorf("edge %d = (%d,%d), want (%d,%d)", i, out.Edges[i].From, out.Edges[i].To, want[i].From, want[i].To)
		}
	}
}

func symbols(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Symbol
	}
	return out
}

func assertSet(t *testing.T, view string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", view, got, want)
	}
	set := map[string]bool{}
	for _, s := range got {
		set[s] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("%s missing %q (got %v)", view, w, got)
		}
	}
}
