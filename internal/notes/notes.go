// Package notes renders an Obsidian markdown map from a contract.Graph. It is
// pure and deterministic: given the same graph and Options, it always produces
// the same output — no wall-clock reads, no randomness. Callers supply
// anything time-dependent (Validated, CommitDate) so the package itself never
// touches time.* or math/rand.
package notes

import "github.com/robot-accomplice/magma/internal/contract"

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
