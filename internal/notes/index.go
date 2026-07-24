package notes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/robot-accomplice/magma/internal/contract"
)

// deadIndex renders the _dead class note: an explanation of what "dead" means
// here plus one bullet per row.
func deadIndex(rows []contract.Row, g contract.Graph, selected map[int]bool) string {
	return rowIndex(
		"Functions with no path from any entrypoint. Every row is a candidate, not a finding — verify reachability before deleting anything.",
		rows, g, selected,
	)
}

// testOnlyIndex renders the _test-only class note: an explanation of what
// "test-only" means here plus one bullet per row.
func testOnlyIndex(rows []contract.Row, g contract.Graph, selected map[int]bool) string {
	return rowIndex(
		"Production functions reached only through tests, never from a production entrypoint. Every row is a candidate, not a finding — verify before treating as untested.",
		rows, g, selected,
	)
}

// rowIndex renders explanation + one bullet per row: a [[wikiTarget]] link
// when the row's node is in selected (it has a note to link to), else a plain
// `file:line` symbol line. Rows are sorted by (File, Line, Symbol) first for
// deterministic output regardless of caller order.
func rowIndex(explanation string, rows []contract.Row, g contract.Graph, selected map[int]bool) string {
	sorted := make([]contract.Row, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].File != sorted[j].File {
			return sorted[i].File < sorted[j].File
		}
		if sorted[i].Line != sorted[j].Line {
			return sorted[i].Line < sorted[j].Line
		}
		return sorted[i].Symbol < sorted[j].Symbol
	})

	type fileLine struct {
		file string
		line int
	}
	byFileLine := make(map[fileLine]contract.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		byFileLine[fileLine{n.File, n.Line}] = n
	}

	var b strings.Builder
	b.WriteString(explanation)
	b.WriteString("\n\n")
	for _, r := range sorted {
		n, ok := byFileLine[fileLine{r.File, r.Line}]
		if ok && selected[n.ID] {
			fmt.Fprintf(&b, "- [[%s]]\n", wikiTarget(n, g.Module))
		} else {
			fmt.Fprintf(&b, "- `%s:%d` %s\n", r.File, r.Line, r.Symbol)
		}
	}
	return b.String()
}

// packagesIndex lists every selected node grouped by its module-relative
// package, one "##" heading per package (sorted), each listing that package's
// function [[wikiTarget]] links (sorted).
func packagesIndex(g contract.Graph, selected map[int]bool) string {
	byPkg := make(map[string][]string)
	for _, n := range g.Nodes {
		if !selected[n.ID] {
			continue
		}
		pkg := relPkg(n.Pkg, g.Module)
		byPkg[pkg] = append(byPkg[pkg], wikiTarget(n, g.Module))
	}

	pkgs := make([]string, 0, len(byPkg))
	for pkg := range byPkg {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)

	var b strings.Builder
	b.WriteString("Packages and the function notes they contain.\n\n")
	for _, pkg := range pkgs {
		links := byPkg[pkg]
		sort.Strings(links)
		fmt.Fprintf(&b, "## %s\n", pkg)
		for _, l := range links {
			fmt.Fprintf(&b, "- [[%s]]\n", l)
		}
		b.WriteString("\n")
	}
	return b.String()
}
