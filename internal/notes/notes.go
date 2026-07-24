// Package notes renders an Obsidian markdown map from a contract.Graph. It is
// pure and deterministic: given the same graph and Options, it always produces
// the same output — no wall-clock reads, no randomness. Callers supply
// anything time-dependent (Validated, CommitDate) so the package itself never
// touches time.* or math/rand.
package notes

import (
	"sort"

	"github.com/robot-accomplice/magma/internal/contract"
)

// Options configures how a graph is rendered into notes.
type Options struct {
	FolderName string // <folder-name>; used in the graph-view filter recipe
	Hotspots   int    // top-N for hotspot lists/pie (default applied by caller; 0 => 10)
	Depth      int    // 0 = all reachable+unreachable (default all); N>0 = forward walk N levels
	From       string // "" = entry points; else a symbol (prettyName) to root the walk
	GraphLink  string // "" = recipe only; else Obsidian vault name for the Advanced-URI one-click link
	Validated  string // preformatted "Last validated" wall-clock string (caller supplies; keeps notes deterministic + testable)
	CommitDate string // from contract.Meta.CommitDate
}

// DegreeRow pairs a node with its in- or out-degree for hotspot lists.
type DegreeRow struct {
	Node   contract.Node
	Degree int
}

// Metrics summarizes a graph for rendering: counts, dead/test-only totals,
// entry points, and degree-ranked hotspots.
type Metrics struct {
	Nodes, Funcs, Methods            int
	Edges, StaticEdges, DynamicEdges int
	Packages                         int
	Reachable, ProdReachable         int
	DeadN, TestOnlyN                 int
	Exported, GeneratedN, TestFuncs  int
	EntryPoints                      []contract.Node // sorted by symbol
	MostCalled, MostCalling          []DegreeRow     // top-N, sorted desc by degree then symbol
}

// Render is the package entrypoint: it produces every markdown file for the map,
// keyed by folder-relative path, plus the sorted manifest of those paths. Pure.
func Render(g contract.Graph, dead, testOnly contract.Note, opts Options) (files map[string]string, manifest []string) {
	if opts.Hotspots == 0 {
		opts.Hotspots = defaultHotspots
	}

	selected := selectNodes(g, opts.Depth, opts.From)
	m := computeMetrics(g, opts.Hotspots)

	files = make(map[string]string)
	files["Overview.md"] = overview(g, dead, testOnly, m, opts)

	if g.Computable {
		files["Dead code.md"] = deadIndex(dead.Rows, g, selected)
		files["Test-only code.md"] = testOnlyIndex(testOnly.Rows, g, selected)
		files["Packages.md"] = packagesIndex(g, selected)

		byID := make(map[int]contract.Node, len(g.Nodes))
		for _, n := range g.Nodes {
			byID[n.ID] = n
		}
		for _, n := range g.Nodes {
			if !selected[n.ID] {
				continue
			}
			var out []contract.Edge
			for _, e := range g.Edges {
				if e.From == n.ID {
					out = append(out, e)
				}
			}
			files[notePath(n, g.Module)] = functionNote(n, out, byID, g.Module, opts.FolderName)
		}
	}

	manifest = make([]string, 0, len(files))
	for k := range files {
		manifest = append(manifest, k)
	}
	sort.Strings(manifest)

	return files, manifest
}

// Reconcile returns the folder-relative paths present in oldManifest but not in
// newManifest — stale notes a caller should delete.
func Reconcile(oldManifest, newManifest []string) []string {
	inNew := make(map[string]bool, len(newManifest))
	for _, p := range newManifest {
		inNew[p] = true
	}

	var stale []string
	for _, p := range oldManifest {
		if !inNew[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	return stale
}
