package architext

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func sampleGraph() contract.Graph {
	return contract.Graph{
		ContractVersion: contract.GraphVersion, Generator: "magma/test",
		Language: "go", Module: "m", SHA: "abc", Tree: "clean",
		Fidelity: "rta", Computable: true,
		Nodes: []contract.Node{
			{ID: 0, Symbol: "main", Pkg: "m", File: "main.go", Line: 7, Kind: "func", Root: true, Reachable: true, ProdReachable: true, FanOut: 1, Signature: &contract.Signature{Params: []contract.Param{}, Results: []contract.Result{}}},
			{ID: 1, Symbol: "Add", Pkg: "m", File: "main.go", Line: 3, Kind: "func", Exported: true, Reachable: true, ProdReachable: true, FanIn: 1, Signature: &contract.Signature{Params: []contract.Param{{Name: "a", Type: "int"}}, Results: []contract.Result{{Type: "int"}}}},
		},
		Edges: []contract.Edge{{From: 0, To: 1, File: "main.go", Line: 7, Kind: "static"}},
	}
}

func TestEmitFineTier(t *testing.T) {
	cg := Emit(sampleGraph())
	if cg.ContractVersion != "magma-code-graph/1" {
		t.Errorf("contract_version = %q", cg.ContractVersion)
	}
	if len(cg.Functions) != 2 || len(cg.Calls) != 1 {
		t.Fatalf("functions=%d calls=%d, want 2 and 1", len(cg.Functions), len(cg.Calls))
	}
	// Functions are sorted by id; "add" < "m-main"? ids are m-main and m-add.
	byID := map[string]Function{}
	for _, f := range cg.Functions {
		byID[f.ID] = f
	}
	add, ok := byID["m-add"]
	if !ok {
		t.Fatalf("no function m-add; got %v", byID)
	}
	if add.Signature.Params[0].Name != "a" || add.FanIn != 1 {
		t.Errorf("Add mapped wrong: %+v", add)
	}
	// The call's endpoints are slugs, not ints.
	if cg.Calls[0].From != "m-main" || cg.Calls[0].To != "m-add" {
		t.Errorf("call endpoints = %s->%s, want m-main->m-add", cg.Calls[0].From, cg.Calls[0].To)
	}
}

func TestEmitRefusal(t *testing.T) {
	g := contract.Graph{ContractVersion: contract.GraphVersion, Language: "python", Computable: false, NotComputableReason: "unsupported language: python"}
	cg := Emit(g)
	if cg.Computable {
		t.Error("refused graph must yield computable=false")
	}
	if cg.NotComputableReason != "unsupported language: python" {
		t.Errorf("reason = %q", cg.NotComputableReason)
	}
	if cg.Functions != nil || cg.Calls != nil {
		t.Error("refused code-graph must have nil functions/calls, not empty slices")
	}
}
