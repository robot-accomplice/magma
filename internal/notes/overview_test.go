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

// TestOverviewDirtyTreeWarning verifies the -dirty warning renders as its OWN
// callout: CommonMark only terminates a blockquote on a truly blank line (no
// leading ">"), so the separator before "> [!warning]" must not itself start
// with ">" or the warning gets swallowed as inert text inside the [!info]
// Provenance box.
func TestOverviewDirtyTreeWarning(t *testing.T) {
	g := fixtureGraph()
	g.Tree = "abc123-dirty"
	m := computeMetrics(g, 10)
	opts := Options{FolderName: "ex", Hotspots: 10, Validated: "2026-07-24 14:06 EDT", CommitDate: "2020-01-01T00:00:00Z"}
	out := overview(g, contract.Note{ReachabilityComputable: true}, contract.Note{ReachabilityComputable: true}, m, opts)
	if !strings.Contains(out, "\n\n> [!warning]") {
		t.Errorf("dirty-tree warning must start its own callout after a truly blank line:\n%s", out)
	}
}

// TestOverviewReachabilityRefusal verifies that when the graph itself IS
// computable but a derived view (here _dead) refused, the Overview surfaces
// that refusal instead of silently showing 0 dead functions.
func TestOverviewReachabilityRefusal(t *testing.T) {
	g := fixtureGraph()
	m := computeMetrics(g, 10)
	opts := Options{FolderName: "ex", Hotspots: 10, Validated: "2026-07-24 14:06 EDT", CommitDate: "2020-01-01T00:00:00Z"}
	dead := contract.Note{ReachabilityComputable: false, NotComputableReason: "no production main in scope; reachability not computable"}
	testOnly := contract.Note{ReachabilityComputable: true}
	out := overview(g, dead, testOnly, m, opts)
	if !strings.Contains(out, "no production main in scope") {
		t.Errorf("refused _dead view must surface its reason, not silently show 0:\n%s", out)
	}
}
