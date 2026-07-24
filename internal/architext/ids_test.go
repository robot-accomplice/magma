package architext

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestAssignIDsStableAndSlugged(t *testing.T) {
	nodes := []contract.Node{
		{ID: 0, Pkg: "internal/backend", Symbol: "BuildGraph", File: "internal/backend/golang.go", Line: 44},
	}
	got := assignIDs(nodes)
	if got[0] != "internal-backend-buildgraph" {
		t.Errorf("id = %q, want internal-backend-buildgraph", got[0])
	}
	// Determinism: same input, same output.
	if again := assignIDs(nodes); again[0] != got[0] {
		t.Error("assignIDs is not deterministic")
	}
}

func TestAssignIDsDisambiguatesCollisions(t *testing.T) {
	// A value method T.M and a pointer method T.M in the same package slug the
	// same; disambiguate deterministically by (file, line) order.
	nodes := []contract.Node{
		{ID: 0, Pkg: "p", Symbol: "T.M", File: "b.go", Line: 20},
		{ID: 1, Pkg: "p", Symbol: "T.M", File: "a.go", Line: 10},
	}
	got := assignIDs(nodes)
	if got[1] != "p-t-m" || got[0] != "p-t-m-2" {
		t.Errorf("collision resolution = %v, want id1=p-t-m (a.go:10 first), id0=p-t-m-2", got)
	}
}
