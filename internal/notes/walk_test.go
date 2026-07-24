package notes

import "testing"

func TestSelectNodes(t *testing.T) {
	g := fixtureGraph()
	all := selectNodes(g, 0, "")
	if len(all) != 4 {
		t.Errorf("depth 0 must select all 4, got %d", len(all))
	}
	// depth 1 from entry points: main (0) + its direct callee A (1)
	d1 := selectNodes(g, 1, "")
	if !d1[0] || !d1[1] || d1[2] || d1[3] {
		t.Errorf("depth 1 wrong: %v", d1)
	}
	// from A, depth 1: A + B
	fa := selectNodes(g, 1, "A")
	if !fa[1] || !fa[2] || fa[0] || fa[3] {
		t.Errorf("from A depth 1 wrong: %v", fa)
	}
}
