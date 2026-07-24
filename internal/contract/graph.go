package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// GraphVersion is the wire-format version of the call graph. Bump only on a
// breaking change to the envelope.
const GraphVersion = "codemap-graph/1"

// Node is one function or method in the program.
type Node struct {
	ID        int    `json:"id"`
	Symbol    string `json:"symbol"` // pretty, package-qualified where useful
	Pkg       string `json:"pkg"`    // import path / module-relative package
	File      string `json:"file"`   // repo-relative
	Line      int    `json:"line"`
	Kind      string `json:"kind"`      // "func" | "method"
	Exported  bool   `json:"exported"`  // visible outside its package
	Test      bool   `json:"test"`      // declared in test code
	Root      bool   `json:"root"`      // an entrypoint (main/init/test runner)
	Generated bool   `json:"generated"` // declared in a generated file

	// Reachable is true when a path exists from any root (production or test).
	Reachable bool `json:"reachable"`
	// ProdReachable is true when a path exists from a NON-test root. A node that
	// is Reachable but not ProdReachable is reached only by tests.
	ProdReachable bool `json:"prod_reachable"`
}

// Edge is a call from one node to another. Kind distinguishes a resolved static
// call from a dynamic (interface/function-value) dispatch.
type Edge struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	File string `json:"site_file"` // where the call is written
	Line int    `json:"site_line"`
	Kind string `json:"kind"` // "static" | "dynamic"
}

// Graph is the primary artifact: the call graph of a repository at one SHA.
// _dead / _test-only notes are derived from it, not computed separately.
type Graph struct {
	ContractVersion     string `json:"contract_version"`
	Generator           string `json:"generator"`
	Language            string `json:"language"`
	Module              string `json:"module"`
	SHA                 string `json:"sha"`
	Tree                string `json:"tree"`
	Fidelity            string `json:"fidelity"` // what an edge MEANS here (e.g. "rta", "syntactic")
	Computable          bool   `json:"computable"`
	NotComputableReason string `json:"not_computable_reason,omitempty"`
	Nodes               []Node `json:"nodes"`
	Edges               []Edge `json:"edges"`
}

// NewGraph seeds an envelope from provenance and language; the backend fills
// Nodes/Edges (or leaves Computable false with a reason).
func NewGraph(m Meta, language, fidelity string) Graph {
	return Graph{
		ContractVersion: GraphVersion,
		Generator:       m.Generator,
		Language:        language,
		SHA:             m.SHA,
		Tree:            m.Tree,
		Fidelity:        fidelity,
		Computable:      true,
	}
}

// Refuse marks a graph as not computable, with a reason. Nodes/Edges stay nil
// (marshal to null), so a refusal is never mistaken for an empty graph.
func (g Graph) Refuse(reason string) Graph {
	g.Computable = false
	g.NotComputableReason = reason
	g.Nodes = nil
	g.Edges = nil
	return g
}

// hasProdRoot reports whether the scope contains a production entrypoint (a
// non-test main). Without one, external callers are outside the analyzed scope,
// so everything looks unreachable and reachability-based views would be almost
// entirely false positives — the concrete failure that made the earlier map
// refuse rather than answer. Root is set only on real `main` functions.
func (g Graph) hasProdRoot() bool {
	for _, n := range g.Nodes {
		if n.Root && !n.Test {
			return true
		}
	}
	return false
}

const noProdMain = "no production main in scope; reachability not computable"

// DeadView derives the _dead note: source nodes reachable from no root. It
// mirrors the graph's computability — a refused graph (or a scope with no
// production main) yields a refused note, so the two artifacts can never
// disagree. Generated and root nodes are excluded (a root is reachable by
// definition; generated code is intentionally not hand-audited for deadness).
func (g Graph) DeadView(m Meta) Note {
	m.Fidelity = g.Fidelity
	if !g.Computable {
		return m.Refused(g.NotComputableReason)
	}
	if !g.hasProdRoot() {
		return m.Refused(noProdMain)
	}
	var rows []Row
	for _, n := range g.Nodes {
		if !n.Reachable && !n.Generated && !n.Root {
			rows = append(rows, Row{Symbol: n.Symbol, File: n.File, Line: n.Line})
		}
	}
	return m.Computed(rows)
}

// TestOnlyView derives the _test-only note: PRODUCTION functions reached only
// through tests (Reachable, not ProdReachable). Functions declared in test files
// are excluded — they are test code, not production code kept alive only by
// tests, and counting them conflates "code that exists for testing" with the
// real signal here: shipped code no production path reaches. Refuses on a
// no-production-main scope for the same reason as DeadView.
func (g Graph) TestOnlyView(m Meta) Note {
	m.Fidelity = g.Fidelity
	if !g.Computable {
		return m.Refused(g.NotComputableReason)
	}
	if !g.hasProdRoot() {
		return m.Refused(noProdMain)
	}
	var rows []Row
	for _, n := range g.Nodes {
		if n.Reachable && !n.ProdReachable && !n.Test && !n.Root && !n.Generated {
			rows = append(rows, Row{Symbol: n.Symbol, File: n.File, Line: n.Line})
		}
	}
	return m.Computed(rows)
}

// WriteGraph marshals the graph to <dir>/graph.json with stable ordering.
func WriteGraph(dir string, g Graph) error {
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(dir, "graph.json"), b, 0o644)
}
