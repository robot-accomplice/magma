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
// date), a prominent graph-filter tip (per-project tag, path fallback, optional
// Advanced-URI one-click link), size, reachability (+ mermaid pie, + caution
// callout if dead>0), surface + entry-point links, hotspots (mermaid pie of call
// concentration + ranked [[link]] lists), an entry-point->package mermaid
// flowchart, a jargon-free note on what a call edge means, and refusal callouts.
// Deterministic except opts.Validated (caller-supplied wall clock).
func overview(g contract.Graph, dead, testOnly contract.Note, m Metrics, opts Options) string {
	var b strings.Builder

	b.WriteString(overviewFrontmatter(opts.FolderName))
	b.WriteString(provenanceCallout(g, opts))

	if !g.Computable {
		fmt.Fprintf(&b, "\n> [!failure] Not computable\n> %s\n", g.NotComputableReason)
		return b.String()
	}

	b.WriteString("\n")
	b.WriteString(graphFilterTip(opts))

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

	b.WriteString("\n## Reading the edges\n\n")
	b.WriteString(edgeNote())

	return b.String()
}

// overviewFrontmatter is the YAML header, versioned so future note formats can
// tell an old Overview.md apart from a new one. Carries the same per-project
// tag (projectTag) stamped on every function note, so the Overview itself is
// included when a reader filters the graph to one project.
func overviewFrontmatter(folderName string) string {
	return "---\n" +
		"magma_notes: magma-notes/1\n" +
		"tags: [magma/overview, " + projectTag(folderName) + "]\n" +
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

	if pie := nonZeroPie([]pieSlice{
		{"Prod-reachable", m.ProdReachable},
		{"Test-only", m.TestOnlyN},
		{"Dead", m.DeadN},
	}); pie != "" {
		b.WriteString("\n")
		b.WriteString(pie)
	}

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

// hotspotsSection renders the top-N most-called pie (top-N only — no
// "(others)" bucket) and the ranked most-called/most-calling [[link]] lists.
// N is opts.Hotspots, defaulting to defaultHotspots when unset.
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

	var b strings.Builder

	slices := make([]pieSlice, 0, len(mostCalled))
	for _, row := range mostCalled {
		slices = append(slices, pieSlice{wikiTarget(row.Node, module), row.Degree})
	}
	if pie := nonZeroPie(slices); pie != "" {
		b.WriteString(pie)
	}

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

// pieSlice is one labeled value in a mermaid pie chart.
type pieSlice struct {
	label string
	value int
}

// nonZeroPie renders a ```mermaid pie showData``` block containing only the
// slices with value > 0 (mermaid pies read oddly with a 0-value wedge, and a
// reader gains nothing from one). Returns "" when no slice is positive, so
// the caller omits the block entirely rather than rendering an empty pie.
func nonZeroPie(slices []pieSlice) string {
	var b strings.Builder
	any := false
	for _, s := range slices {
		if s.value <= 0 {
			continue
		}
		if !any {
			b.WriteString("```mermaid\npie showData\n")
			any = true
		}
		fmt.Fprintf(&b, "  %q : %d\n", s.label, s.value)
	}
	if !any {
		return ""
	}
	b.WriteString("```\n")
	return b.String()
}

// graphFilterTip renders a prominent `> [!tip]` callout — placed right after
// Provenance, before any other section, so the per-project graph filter is
// the first actionable thing a reader sees rather than buried at the bottom.
// The first line carries the tag BARE (no backticks/code formatting): Obsidian
// only renders a bare "#tag" as a live, clickable tag that opens search — a
// backticked or code-fenced tag is inert text. The second line repeats the
// tag inside a backticked `tag:#...` paste-string for the graph-view filter
// box (which needs the "tag:" prefix, unlike the clickable form), with the
// path filter as a fallback, plus an optional Advanced-URI one-click link.
func graphFilterTip(opts Options) string {
	tag := projectTag(opts.FolderName)
	var b strings.Builder
	b.WriteString("> [!tip] See just this project's graph\n")
	fmt.Fprintf(&b, "> Click this tag to list every note in this project: #%s\n", tag)
	fmt.Fprintf(&b, "> To scope the graph view (⌘/Ctrl-G), paste into the graph filter: `tag:#%s` (or `path:\"%s/nodes\"`).\n", tag, opts.FolderName)
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

// flowNode is one mermaid flowchart node: a stable, syntax-safe id plus the
// human-readable label to display inside it (`id["label"]`).
type flowNode struct {
	id    string
	label string
}

// entryPackageFlowchart renders a mermaid flowchart from each entry point to
// the distinct packages it transitively reaches (module-relative, sorted,
// deduped). Kept top-level (packages, not functions) and capped per entry
// point so it stays legible even for a large graph. Node ids are sanitized
// ("e_"/"p_" prefix + [A-Za-z0-9_] only) since mermaid flowchart node ids
// cannot themselves be quoted strings; the human-readable text goes in the
// `id["label"]` bracket form instead.
func entryPackageFlowchart(g contract.Graph, entryPoints []contract.Node) string {
	byID := make(map[int]contract.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	adj := make(map[int][]int, len(g.Nodes))
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	type flowEdge struct{ src, dst flowNode }
	seen := make(map[[2]string]bool) // keyed by (src.id, dst.id)
	var edges []flowEdge
	truncated := false

	for _, ep := range entryPoints {
		src := flowNode{"e_" + sanitizeMermaidID(ep.Symbol), ep.Symbol}

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
			idBase, label := p, p
			if p == "" {
				idBase, label = "root", "(root)"
			}
			dst := flowNode{"p_" + sanitizeMermaidID(idBase), label}

			key := [2]string{src.id, dst.id}
			if !seen[key] {
				seen[key] = true
				edges = append(edges, flowEdge{src, dst})
			}
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].src.id != edges[j].src.id {
			return edges[i].src.id < edges[j].src.id
		}
		return edges[i].dst.id < edges[j].dst.id
	})

	var b strings.Builder
	if len(edges) > 0 {
		b.WriteString("```mermaid\nflowchart LR\n")
		for _, e := range edges {
			fmt.Fprintf(&b, "  %s[%q] --> %s[%q]\n", e.src.id, e.src.label, e.dst.id, e.dst.label)
		}
		b.WriteString("```\n")
	}
	if truncated {
		b.WriteString("\n_(truncated: some entry points reach more packages than shown)_\n")
	}
	return b.String()
}

// sanitizeMermaidID replaces every character not safe in a mermaid flowchart
// node id ([A-Za-z0-9_]) with "_", so a symbol or package name containing
// slashes, dots, or other punctuation can never break the diagram.
func sanitizeMermaidID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
