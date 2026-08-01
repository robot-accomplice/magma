package notes

import (
	"sort"

	"github.com/robot-accomplice/magma/internal/contract"
)

// computeMetrics summarizes g: counts, dead/test-only totals (mirroring
// contract.DeadView / contract.TestOnlyView's predicates), entry points, and
// the top-N in-/out-degree hotspots. Pure: no time, no randomness, and all
// slices are sorted before returning for deterministic output.
func computeMetrics(g contract.Graph, topN int) Metrics {
	var m Metrics

	m.Nodes = len(g.Nodes)
	m.Edges = len(g.Edges)

	pkgs := make(map[string]struct{})
	byID := make(map[int]contract.Node, len(g.Nodes))
	var entryPoints []contract.Node

	for _, n := range g.Nodes {
		byID[n.ID] = n
		pkgs[n.Pkg] = struct{}{}

		switch n.Kind {
		// "init" is the Rust backend's synthesized const/static initializer
		// node. It is counted as a function because it IS one — Go's own
		// `init#N` nodes arrive here as "func" already, so folding it in keeps
		// the two languages tallying the same thing. Counting it nowhere,
		// which is what an unlisted kind does, would silently make
		// Funcs+Methods < len(Nodes) on every Rust map.
		case "func", "init":
			m.Funcs++
		case "method":
			m.Methods++
		}
		if n.Exported {
			m.Exported++
		}
		if n.Generated {
			m.GeneratedN++
		}
		if n.Test {
			m.TestFuncs++
		}
		if n.Reachable {
			m.Reachable++
		}
		if n.ProdReachable {
			m.ProdReachable++
		}
		if !n.Reachable && !n.Generated && !n.Root {
			m.DeadN++
		}
		if n.Reachable && !n.ProdReachable && !n.Test && !n.Root && !n.Generated {
			m.TestOnlyN++
		}
		if n.Root && !n.Test {
			entryPoints = append(entryPoints, n)
		}
	}
	m.Packages = len(pkgs)

	sort.Slice(entryPoints, func(i, j int) bool { return entryPoints[i].Symbol < entryPoints[j].Symbol })
	m.EntryPoints = entryPoints

	inDegree := make(map[int]int)
	outDegree := make(map[int]int)
	for _, e := range g.Edges {
		switch e.Kind {
		case "static":
			m.StaticEdges++
		case "dynamic":
			m.DynamicEdges++
		}
		outDegree[e.From]++
		inDegree[e.To]++
	}

	m.MostCalled = topDegreeRows(byID, inDegree, topN)
	m.MostCalling = topDegreeRows(byID, outDegree, topN)

	return m
}

// topDegreeRows builds the top-N DegreeRow list from a degree map, sorted by
// degree descending, ties broken by symbol ascending. Nodes with zero degree
// are excluded — they aren't hotspots.
func topDegreeRows(byID map[int]contract.Node, degree map[int]int, topN int) []DegreeRow {
	rows := make([]DegreeRow, 0, len(degree))
	for id, d := range degree {
		if d == 0 {
			continue
		}
		rows = append(rows, DegreeRow{Node: byID[id], Degree: d})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Degree != rows[j].Degree {
			return rows[i].Degree > rows[j].Degree
		}
		return rows[i].Node.Symbol < rows[j].Node.Symbol
	})
	if topN > 0 && len(rows) > topN {
		rows = rows[:topN]
	}
	return rows
}
