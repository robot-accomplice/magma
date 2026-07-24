package notes

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestNotePathAndWikiTarget(t *testing.T) {
	n := contract.Node{Symbol: "BuildGraph", Pkg: "github.com/x/m/internal/backend"}
	if got := notePath(n, "github.com/x/m"); got != "nodes/internal/backend/BuildGraph.md" {
		t.Errorf("notePath = %q", got)
	}
	if got := wikiTarget(n, "github.com/x/m"); got != "internal/backend/BuildGraph" {
		t.Errorf("wikiTarget = %q", got)
	}
	// root package (pkg == module) collapses to nodes/<symbol>
	r := contract.Node{Symbol: "main", Pkg: "github.com/x/m"}
	if got := notePath(r, "github.com/x/m"); got != "nodes/main.md" {
		t.Errorf("root-pkg notePath = %q", got)
	}
}
