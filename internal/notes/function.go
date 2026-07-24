package notes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/robot-accomplice/magma/internal/contract"
)

// functionNote renders one node's markdown: YAML frontmatter + a file:line pointer +
// a "## Calls" list of [[callee]] links (deduped, sorted; dynamic edges annotated).
// callees are the node's outgoing edges resolved to nodes. folderName stamps the
// per-project tag (projectTag) so a vault holding several maps can filter its
// graph view to just this one.
func functionNote(n contract.Node, callees []contract.Edge, byID map[int]contract.Node, module, folderName string) string {
	dead := !n.Reachable && !n.Generated && !n.Root
	testOnly := n.Reachable && !n.ProdReachable && !n.Test && !n.Root && !n.Generated

	tags := []string{"magma/node", projectTag(folderName)}
	if dead {
		tags = append(tags, "magma/dead")
	}
	if testOnly {
		tags = append(tags, "magma/test-only")
	}
	if n.Root {
		tags = append(tags, "magma/root")
	}
	if n.Generated {
		tags = append(tags, "magma/generated")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "pkg: %s\n", n.Pkg)
	fmt.Fprintf(&b, "file: %s\n", n.File)
	fmt.Fprintf(&b, "line: %d\n", n.Line)
	fmt.Fprintf(&b, "kind: %s\n", n.Kind)
	fmt.Fprintf(&b, "exported: %t\n", n.Exported)
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(tags, ", "))
	fmt.Fprintf(&b, "---\n\n")

	fmt.Fprintf(&b, "`%s:%d`\n", n.File, n.Line)

	if links := calleeLinks(callees, byID, module); len(links) > 0 {
		b.WriteString("\n## Calls\n")
		for _, l := range links {
			fmt.Fprintf(&b, "- [[%s]]\n", l)
		}
	}

	return b.String()
}

// calleeLinks resolves n's outgoing edges to deduped [[target]] link strings,
// sorted by target. Multiple edges to the same callee are aggregated: the
// target is annotated " (dynamic)" only when every edge reaching it is
// dynamic (no static resolution exists). Targets are module-relative, via
// wikiTarget, matching notePath so [[...]] links resolve to the callee's note.
func calleeLinks(callees []contract.Edge, byID map[int]contract.Node, module string) []string {
	type agg struct {
		anyStatic  bool
		anyDynamic bool
	}
	byTarget := make(map[string]*agg)

	for _, e := range callees {
		callee, ok := byID[e.To]
		if !ok {
			continue
		}
		target := wikiTarget(callee, module)
		a, ok := byTarget[target]
		if !ok {
			a = &agg{}
			byTarget[target] = a
		}
		if e.Kind == "dynamic" {
			a.anyDynamic = true
		} else {
			a.anyStatic = true
		}
	}

	targets := make([]string, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	links := make([]string, 0, len(targets))
	for _, target := range targets {
		l := target
		if a := byTarget[target]; a.anyDynamic && !a.anyStatic {
			l += " (dynamic)"
		}
		links = append(links, l)
	}
	return links
}
