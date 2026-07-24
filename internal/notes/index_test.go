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
