package notes

import (
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestIndexNotes(t *testing.T) {
	g := fixtureGraph()
	sel := map[int]bool{0: true, 1: true, 2: true, 3: true}
	dead := deadIndex([]contract.Row{{Symbol: "C", File: "c.go", Line: 3}}, g, sel)
	if !strings.Contains(dead, "[[C]]") || !strings.Contains(dead, "candidate") {
		t.Errorf("dead index wrong:\n%s", dead)
	}
	pkgs := packagesIndex(g, sel)
	if !strings.Contains(pkgs, "[[sub/B]]") {
		t.Errorf("packages index missing B:\n%s", pkgs)
	}
}

// A node whose package equals the module root (relPkg returns "") must render
// as "## (root)", never a bare "## " heading. fixtureGraph's node 0 ("main")
// has Pkg "ex" == Module "ex", so it IS the root package.
func TestPackagesIndexRootHeading(t *testing.T) {
	g := fixtureGraph()
	sel := map[int]bool{0: true}

	pkgs := packagesIndex(g, sel)
	if !strings.Contains(pkgs, "## (root)\n") {
		t.Errorf("packages index missing root heading:\n%s", pkgs)
	}
	if strings.Contains(pkgs, "## \n") {
		t.Errorf("packages index has a bare heading:\n%s", pkgs)
	}
}
