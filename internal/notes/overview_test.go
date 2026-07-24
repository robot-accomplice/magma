package notes

import (
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestOverview(t *testing.T) {
	g := fixtureGraph()
	m := computeMetrics(g, 10)
	opts := Options{FolderName: "ex", Hotspots: 10, Validated: "2026-07-24 14:06 EDT", CommitDate: "2020-01-01T00:00:00Z"}
	out := overview(g, contract.Note{ReachabilityComputable: true}, contract.Note{ReachabilityComputable: true}, m, opts)
	for _, want := range []string{
		"magma-notes/1", // frontmatter version marker
		"Last validated", "2026-07-24 14:06 EDT",
		"abc123",            // tree
		"```mermaid", "pie", // a mermaid pie is present
		"flowchart",                 // entry-point -> package mermaid flowchart
		"RTA call graph",            // fidelity in plain English
		"⌘/Ctrl", `path:"ex/nodes"`, // graph-view recipe
		"[[sub/B]]", // a hotspot link (module-relative wikiTarget)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q in:\n%s", want, out)
		}
	}
}

func TestOverviewRefusalCallout(t *testing.T) {
	g := contract.NewGraph(contract.Meta{Tree: "abc"}, "rust", "").Refuse(`language "rust" not built yet`)
	out := overview(g, contract.Note{}, contract.Note{}, Metrics{}, Options{FolderName: "p"})
	if !strings.Contains(out, "[!failure]") || !strings.Contains(out, "rust") {
		t.Errorf("refused graph must show a failure callout:\n%s", out)
	}
}
