package notes

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func fixtureGraph() contract.Graph {
	g := contract.NewGraph(contract.Meta{Tree: "abc123", Fidelity: "rta"}, "go", "rta")
	g.Module = "ex"
	g.Nodes = []contract.Node{
		{ID: 0, Symbol: "main", Pkg: "ex", File: "main.go", Line: 1, Kind: "func", Root: true, Reachable: true, ProdReachable: true, Exported: false},
		{ID: 1, Symbol: "A", Pkg: "ex", File: "a.go", Line: 5, Kind: "func", Exported: true, Reachable: true, ProdReachable: true},
		{ID: 2, Symbol: "B", Pkg: "ex/sub", File: "sub/b.go", Line: 9, Kind: "func", Reachable: true, ProdReachable: true},
		{ID: 3, Symbol: "C", Pkg: "ex", File: "c.go", Line: 3, Kind: "func"}, // dead
	}
	g.Edges = []contract.Edge{
		{From: 0, To: 1, Kind: "static"}, {From: 1, To: 2, Kind: "static"}, {From: 3, To: 2, Kind: "dynamic"},
	}
	return g
}

func TestComputeMetrics(t *testing.T) {
	m := computeMetrics(fixtureGraph(), 10)
	if m.Nodes != 4 || m.Edges != 3 || m.Packages != 2 {
		t.Errorf("counts wrong: %+v", m)
	}
	if m.DeadN != 1 { // C
		t.Errorf("DeadN = %d, want 1", m.DeadN)
	}
	if len(m.EntryPoints) != 1 || m.EntryPoints[0].Symbol != "main" {
		t.Errorf("entry points wrong: %+v", m.EntryPoints)
	}
	// B has in-degree 2 (from A and C) -> most-called first
	if len(m.MostCalled) == 0 || m.MostCalled[0].Node.Symbol != "B" || m.MostCalled[0].Degree != 2 {
		t.Errorf("most-called wrong: %+v", m.MostCalled)
	}
}
