package notes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/robot-accomplice/magma/internal/contract"
)

// defaultHotspots is used when Options.Hotspots is unset (0).
const defaultHotspots = 10

// maxFlowchartPackages caps how many packages a single entry point contributes
// to the entry-point->package mermaid flowchart, so a large repo's dashboard
// stays legible instead of drowning in edges.
const maxFlowchartPackages = 20

// overview renders Overview.md: provenance callout (incl. Last validated + commit
// date), size, reachability (+ mermaid pie, + caution callout if dead>0), surface +
// entry-point links, hotspots (mermaid pie of call concentration + ranked [[link]]
// lists), an entry-point->package mermaid flowchart, a graph-view recipe (+
// optional Advanced-URI one-click link), a jargon-free note on what a call edge
// means, and refusal callouts. Deterministic except opts.Validated
// (caller-supplied wall clock).
func overview(g contract.Graph, dead, testOnly contract.Note, m Metrics, opts Options) string {
	var b strings.Builder

	b.WriteString(overviewFrontmatter())
	b.WriteString(provenanceCallout(g, opts))

	if !g.Computable {
		fmt.Fprintf(&b, "\n> [!failure] Not computable\n> %s\n", g.NotComputableReason)
		return b.String()
	}

	b.WriteString("\n## Size\n\n")
	b.WriteString(sizeTable(m))

	b.WriteString("\n## Reachability\n\n")
	b.WriteString(reachabilitySection(m, dead, testOnly))

	b.WriteString("\n## Surface\n\n")
	b.WriteString(surfaceSection(m, g.Module))

	b.WriteString("\n## Hotspots\n\n")
	b.WriteString(hotspotsSection(m, opts, g.Module))

	b.WriteString("\n## Entry points -> packages\n\n")
	b.WriteString(entryPackageFlowchart(g, m.EntryPoints))

	b.WriteString("\n## Graph view\n\n")
	b.WriteString(graphViewRecipe(opts))

	b.WriteString("\n## Reading the edges\n\n")
	b.WriteString(edgeNote())

	return b.String()
}

// overviewFrontmatter is the YAML header, versioned so future note formats can
// tell an old Overview.md apart from a new one.
func overviewFrontmatter() string {
	return "---\n" +
		"magma_notes: magma-notes/1\n" +
		"tags: [magma/overview]\n" +
		"---\n\n"
}

// provenanceCallout renders the repo/module/language/tree/commit-date/version/
// validated-at callout. A `-dirty` tree gets an additional warning callout.
func provenanceCallout(g contract.Graph, opts Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "> [!info] Provenance\n")
	fmt.Fprintf(&b, "> Module: `%s`\n", g.Module)
	fmt.Fprintf(&b, "> Language: %s\n", g.Language)
	fmt.Fprintf(&b, "> Tree: `%s`\n", g.Tree)
	fmt.Fprintf(&b, "> Commit date: %s\n", opts.CommitDate)
	fmt.Fprintf(&b, "> Magma version: %s\n", g.Generator)
	fmt.Fprintf(&b, "> Last validated: %s\n", opts.Validated)

	if strings.HasSuffix(g.Tree, "-dirty") {
		b.WriteString("\n> [!warning] Dirty working tree\n> This map was built from an uncommitted working tree; it can't be reproduced from its SHA.\n")
	}
	return b.String()
}

// sizeTable renders the nodes/funcs/methods/edges/packages table.
func sizeTable(m Metrics) string {
	var b strings.Builder
	b.WriteString("| Metric | Count |\n")
	b.WriteString("|---|---|\n")
	fmt.Fprintf(&b, "| Nodes | %d |\n", m.Nodes)
	fmt.Fprintf(&b, "| Funcs | %d |\n", m.Funcs)
	fmt.Fprintf(&b, "| Methods | %d |\n", m.Methods)
	fmt.Fprintf(&b, "| Edges (static) | %d |\n", m.StaticEdges)
	fmt.Fprintf(&b, "| Edges (dynamic) | %d |\n", m.DynamicEdges)
	fmt.Fprintf(&b, "| Packages | %d |\n", m.Packages)
	return b.String()
}

// reachabilitySection renders the reachable/prod-reachable/dead/test-only
// counts + pie + caution callout, or a failure callout in place of them when
// either view refused to compute.
func reachabilitySection(m Metrics, dead, testOnly contract.Note) string {
	var b strings.Builder

	if !dead.ReachabilityComputable || !testOnly.ReachabilityComputable {
		reason := dead.NotComputableReason
		if reason == "" {
			reason = testOnly.NotComputableReason
		}
		fmt.Fprintf(&b, "> [!failure] Reachability not computable\n> %s\n", reason)
		return b.String()
	}

	fmt.Fprintf(&b, "- Reachable: %d\n", m.Reachable)
	fmt.Fprintf(&b, "- Prod-reachable: %d\n", m.ProdReachable)
	fmt.Fprintf(&b, "- Dead: %d ([[Dead code]])\n", m.DeadN)
	fmt.Fprintf(&b, "- Test-only: %d ([[Test-only code]])\n", m.TestOnlyN)

	b.WriteString("\n```mermaid\npie showData\n")
	fmt.Fprintf(&b, "  \"Prod-reachable\" : %d\n", m.ProdReachable)
	fmt.Fprintf(&b, "  \"Test-only\" : %d\n", m.TestOnlyN)
	fmt.Fprintf(&b, "  \"Dead\" : %d\n", m.DeadN)
	b.WriteString("```\n")

	if m.DeadN > 0 {
		fmt.Fprintf(&b, "\n> [!caution] %d dead function(s) detected. See [[Dead code]].\n", m.DeadN)
	}
	return b.String()
}

// surfaceSection renders the exported count and entry-point [[links]].
func surfaceSection(m Metrics, module string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- Exported: %d\n", m.Exported)
	b.WriteString("- Entry points:\n")
	for _, n := range m.EntryPoints {
		fmt.Fprintf(&b, "  - [[%s]]\n", wikiTarget(n, module))
	}
	return b.String()
}

// hotspotsSection renders the top-N most-called pie (+ "(others)" slice) and
// the ranked most-called/most-calling [[link]] lists. N is opts.Hotspots,
// defaulting to defaultHotspots when unset.
func hotspotsSection(m Metrics, opts Options, module string) string {
	n := opts.Hotspots
	if n <= 0 {
		n = defaultHotspots
	}

	mostCalled := m.MostCalled
	if len(mostCalled) > n {
		mostCalled = mostCalled[:n]
	}
	mostCalling := m.MostCalling
	if len(mostCalling) > n {
		mostCalling = mostCalling[:n]
	}

	shown := 0
	for _, row := range mostCalled {
		shown += row.Degree
	}
	others := m.Edges - shown
	if others < 0 {
		others = 0
	}

	var b strings.Builder
	b.WriteString("```mermaid\npie showData\n")
	for _, row := range mostCalled {
		fmt.Fprintf(&b, "  %q : %d\n", wikiTarget(row.Node, module), row.Degree)
	}
	fmt.Fprintf(&b, "  \"(others)\" : %d\n", others)
	b.WriteString("```\n")

	b.WriteString("\n**Most called (in-degree):**\n")
	for _, row := range mostCalled {
		fmt.Fprintf(&b, "- [[%s]] — %d\n", wikiTarget(row.Node, module), row.Degree)
	}

	b.WriteString("\n**Most calling (out-degree):**\n")
	for _, row := range mostCalling {
		fmt.Fprintf(&b, "- [[%s]] — %d\n", wikiTarget(row.Node, module), row.Degree)
	}

	return b.String()
}

// graphViewRecipe renders the always-present "open the graph view and paste
// this filter" recipe, plus an optional Advanced-URI one-click link.
func graphViewRecipe(opts Options) string {
	var b strings.Builder
	b.WriteString("Open the graph view (⌘/Ctrl-G) and paste this filter to scope it to this map:\n\n")
	fmt.Fprintf(&b, "```\npath:\"%s/nodes\"\n```\n", opts.FolderName)
	if opts.GraphLink != "" {
		fmt.Fprintf(&b, "\n[One-click open](obsidian://advanced-uri?vault=%s&commandid=graph%%3Aopen)\n", opts.GraphLink)
	}
	return b.String()
}

// edgeNote explains what an edge means in plain, jargon-free English: no
// mention of "fidelity" or the analysis technique's name, just what a reader
// can and can't trust about a call.
func edgeNote() string {
	return "> [!note] Reading the edges\n" +
		"> Direct calls are exact. Calls through interfaces or function values are approximated — they may include paths that can't occur at runtime, but never miss a real one.\n"
}

// entryPackageFlowchart renders a mermaid flowchart from each entry point to
// the distinct packages it transitively reaches (module-relative, sorted,
// deduped). Kept top-level (packages, not functions) and capped per entry
// point so it stays legible even for a large graph.
func entryPackageFlowchart(g contract.Graph, entryPoints []contract.Node) string {
	byID := make(map[int]contract.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	adj := make(map[int][]int, len(g.Nodes))
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	type flowEdge struct{ from, to string }
	seen := make(map[flowEdge]bool)
	var edges []flowEdge
	truncated := false

	for _, ep := range entryPoints {
		epLabel := wikiTarget(ep, g.Module)

		visited := map[int]bool{ep.ID: true}
		queue := []int{ep.ID}
		pkgSet := make(map[string]bool)
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, next := range adj[id] {
				if visited[next] {
					continue
				}
				visited[next] = true
				queue = append(queue, next)
				if n, ok := byID[next]; ok {
					pkgSet[relPkg(n.Pkg, g.Module)] = true
				}
			}
		}

		pkgs := make([]string, 0, len(pkgSet))
		for p := range pkgSet {
			pkgs = append(pkgs, p)
		}
		sort.Strings(pkgs)
		if len(pkgs) > maxFlowchartPackages {
			pkgs = pkgs[:maxFlowchartPackages]
			truncated = true
		}

		for _, p := range pkgs {
			label := p
			if label == "" {
				label = "(root)"
			}
			fe := flowEdge{epLabel, label}
			if !seen[fe] {
				seen[fe] = true
				edges = append(edges, fe)
			}
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
	})

	var b strings.Builder
	b.WriteString("```mermaid\nflowchart LR\n")
	for _, e := range edges {
		fmt.Fprintf(&b, "  %q --> %q\n", e.from, e.to)
	}
	b.WriteString("```\n")
	if truncated {
		b.WriteString("\n_(truncated: some entry points reach more packages than shown)_\n")
	}
	return b.String()
}
