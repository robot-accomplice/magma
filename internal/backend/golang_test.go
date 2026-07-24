package backend

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
)

var fixtureMeta = contract.Meta{Generator: "magma/test", SHA: "deadbee", Tree: "deadbee", Fidelity: "rta"}

// The Go backend registers itself for detect.Go on init.
func TestGoBackendRegistered(t *testing.T) {
	b, ok := For(detect.Go)
	if !ok {
		t.Fatal("no backend registered for Go")
	}
	if b.Language() != "go" {
		t.Errorf("Language() = %q, want %q", b.Language(), "go")
	}
}

// The live fixture (main -> Live -> T.M, plus Dead, *T.P, and test-only OnlyTest)
// produces a computable RTA graph whose derived views exactly separate dead code
// from test-only code. This is the end-to-end check on the real x/tools analysis.
func TestBuildGraphLiveModule(t *testing.T) {
	g := buildFixture(t, "livemod")

	if !g.Computable {
		t.Fatalf("graph must be computable, got refusal: %s", g.NotComputableReason)
	}
	if g.Fidelity != "rta" || g.Language != "go" {
		t.Errorf("graph meta wrong: language=%q fidelity=%q", g.Language, g.Fidelity)
	}
	if g.Tree != "deadbee" {
		t.Errorf("provenance not carried: Tree=%q", g.Tree)
	}

	byName := map[string]contract.Node{}
	for _, n := range g.Nodes {
		byName[n.Symbol] = n
	}

	// A real production root (func main) must be present and flagged.
	if m, ok := byName["main"]; !ok || !m.Root || m.Test {
		t.Errorf("main node = %+v (ok=%v), want a non-test Root", byName["main"], ok)
	}

	// The reached method is a method node; the pointer-receiver method renders
	// via prettyName's receiver branch.
	if m, ok := byName["T.M"]; !ok || m.Kind != "method" {
		t.Errorf("T.M node = %+v (ok=%v), want kind=method", byName["T.M"], ok)
	}
	if _, ok := byName["T.P"]; !ok {
		t.Error("expected pointer-receiver method to render as T.P")
	}

	// Dead view = unreached source (Dead func + the unreached pointer method).
	assertSymbolSet(t, "_dead", g.DeadView(fixtureMeta).Rows, []string{"Dead", "T.P"})
	// Test-only view = production code reached only by tests.
	assertSymbolSet(t, "_test-only", g.TestOnlyView(fixtureMeta).Rows, []string{"OnlyTest"})

	// The static call Live -> T.M must appear as a static edge between the mapped nodes.
	if !hasStaticEdge(g, byName["Live"].ID, byName["T.M"].ID) {
		t.Error("expected a static edge Live -> T.M")
	}
}

// A module with no production main yields a computable graph but views that
// refuse — reachability from a test-only root would be almost all false positives.
func TestBuildGraphNoProductionMainRefusesViews(t *testing.T) {
	g := buildFixture(t, "libmod")

	if !g.Computable {
		t.Fatalf("library graph should still be computable, got: %s", g.NotComputableReason)
	}
	if len(g.Nodes) == 0 {
		t.Fatal("library graph should still emit nodes")
	}

	for name, n := range map[string]contract.Note{"_dead": g.DeadView(fixtureMeta), "_test-only": g.TestOnlyView(fixtureMeta)} {
		if n.ReachabilityComputable {
			t.Errorf("%s: view must refuse without a production main", name)
		}
		if n.NotComputableReason == "" {
			t.Errorf("%s: refusal must carry a reason", name)
		}
	}
}

func buildFixture(t *testing.T, name string) contract.Graph {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	b, ok := For(detect.Go)
	if !ok {
		t.Fatal("Go backend not registered")
	}
	g, err := b.BuildGraph(repo, fixtureMeta, nil)
	if err != nil {
		t.Fatalf("BuildGraph(%s): %v", name, err)
	}
	return g
}

// BuildGraph reports its phases through the progress callback so the CLI can show
// a live status line.
func TestBuildGraphReportsProgress(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("testdata", "livemod"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := For(detect.Go)
	var stages []string
	spy := func(s string) { stages = append(stages, s) }
	if _, err := b.BuildGraph(repo, fixtureMeta, spy); err != nil {
		t.Fatal(err)
	}
	if len(stages) == 0 {
		t.Fatal("expected progress stages, got none")
	}
	joined := strings.Join(stages, "|")
	for _, want := range []string{"loading", "reachability", "edges"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress missing a %q stage; got %v", want, stages)
		}
	}
}

func TestBuildGraphStampsModule(t *testing.T) {
	g := buildFixture(t, "livemod")
	if g.Module != "magmafixture" {
		t.Errorf("Module = %q, want %q", g.Module, "magmafixture")
	}
}

// Per-function signatures (param name+type, result types) and the doc synopsis
// are extracted from the SSA type info and the declaration's comment.
func TestBuildGraphExtractsSignatureAndDoc(t *testing.T) {
	g := buildFixture(t, "sigmod")

	var add *contract.Node
	for i := range g.Nodes {
		if g.Nodes[i].Symbol == "Add" {
			add = &g.Nodes[i]
		}
	}
	if add == nil {
		t.Fatal("no node for Add")
	}
	if add.Signature == nil {
		t.Fatal("Add has no signature")
	}
	wantParams := []contract.Param{{Name: "a", Type: "int"}, {Name: "b", Type: "int"}}
	if !reflect.DeepEqual(add.Signature.Params, wantParams) {
		t.Errorf("params = %+v, want %+v", add.Signature.Params, wantParams)
	}
	if len(add.Signature.Results) != 1 || add.Signature.Results[0].Type != "int" {
		t.Errorf("results = %+v, want [{int}]", add.Signature.Results)
	}
	if add.Doc != "Add returns the sum of a and b." {
		t.Errorf("doc = %q, want the first sentence of the comment", add.Doc)
	}
}

func TestBuildGraphComputesFan(t *testing.T) {
	g := buildFixture(t, "sigmod")
	fan := map[string][2]int{} // symbol -> {fanIn, fanOut}
	for _, n := range g.Nodes {
		fan[n.Symbol] = [2]int{n.FanIn, n.FanOut}
	}
	// main calls Add and sq (sq twice, deduped to one edge) -> fanOut 2, fanIn 0.
	if fan["main"] != [2]int{0, 2} {
		t.Errorf("main fan = %v, want {0,2}", fan["main"])
	}
	// Add is called once by main, calls nothing -> fanIn 1, fanOut 0.
	if fan["Add"] != [2]int{1, 0} {
		t.Errorf("Add fan = %v, want {1,0}", fan["Add"])
	}
}

func hasStaticEdge(g contract.Graph, from, to int) bool {
	for _, e := range g.Edges {
		if e.From == from && e.To == to && e.Kind == "static" {
			return true
		}
	}
	return false
}

func assertSymbolSet(t *testing.T, view string, rows []contract.Row, want []string) {
	t.Helper()
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Symbol] = true
	}
	if len(got) != len(want) {
		names := make([]string, 0, len(got))
		for s := range got {
			names = append(names, s)
		}
		t.Fatalf("%s = %v, want exactly %v", view, names, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s missing %q", view, w)
		}
	}
}
