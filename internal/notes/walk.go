package notes

import "github.com/robot-accomplice/magma/internal/contract"

// selectNodes returns the set of node IDs that get a per-function note.
// Depth 0 and From "" => every node. Depth N>0 => nodes within N call-levels of the
// roots (entry points, or the From symbol if set), by forward BFS over edges.
func selectNodes(g contract.Graph, depth int, from string) map[int]bool {
	if depth <= 0 && from == "" {
		all := make(map[int]bool, len(g.Nodes))
		for _, n := range g.Nodes {
			all[n.ID] = true
		}
		return all
	}

	var roots []int
	if from != "" {
		for _, n := range g.Nodes {
			if n.Symbol == from {
				roots = append(roots, n.ID)
			}
		}
	} else {
		for _, n := range g.Nodes {
			if n.Root && !n.Test {
				roots = append(roots, n.ID)
			}
		}
	}

	adj := make(map[int][]int, len(g.Nodes))
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	// depth<=0 with a non-empty from means unlimited depth from that root.
	unlimited := depth <= 0
	level := make(map[int]int, len(roots))
	queue := make([]int, 0, len(roots))
	for _, r := range roots {
		if _, seen := level[r]; !seen {
			level[r] = 0
			queue = append(queue, r)
		}
	}

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		lvl := level[id]
		if !unlimited && lvl >= depth {
			continue
		}
		for _, next := range adj[id] {
			if _, seen := level[next]; !seen {
				level[next] = lvl + 1
				queue = append(queue, next)
			}
		}
	}

	selected := make(map[int]bool, len(level))
	for id := range level {
		selected[id] = true
	}
	return selected
}
