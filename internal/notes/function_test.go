package notes

import (
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestFunctionNote(t *testing.T) {
	g := fixtureGraph()
	byID := map[int]contract.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	out := functionNote(byID[1], []contract.Edge{{From: 1, To: 2, Kind: "static"}}, byID, "ex")
	for _, want := range []string{
		"---", "pkg: ex", "file: a.go", "line: 5", "kind: func", "exported: true",
		"`a.go:5`", "## Calls", "[[sub/B]]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("note missing %q in:\n%s", want, out)
		}
	}
}
