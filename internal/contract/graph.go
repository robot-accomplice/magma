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

// Param is one function parameter: its name (may be empty for unnamed params)
// and its module-relative type string (e.g. "contract.Graph", "int").
type Param struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type"`
}

// Result is one function result type. Result names are intentionally dropped —
// the type is the architecture-relevant fact.
type Result struct {
	Type string `json:"type"`
}

// Signature is a function's parameters and results, as rendered by the backend.
type Signature struct {
	Params  []Param  `json:"params"`
	Results []Result `json:"results"`
}

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

	// Signature is the function's parameters and results. Nil for backends that
	// do not (yet) extract signatures. Set for every Go node.
	Signature *Signature `json:"signature,omitempty"`
	// Doc is the first sentence of the declaration's doc comment ("" if none).
	Doc string `json:"doc,omitempty"`
	// FanIn / FanOut are the distinct in/out call-edge counts, derived from the graph.
	FanIn  int `json:"fan_in"`
	FanOut int `json:"fan_out"`
}

// IsDead reports whether this node is production-dead: reachable from no root.
// Roots are reachable by definition; generated code is not hand-audited for
// deadness. This is the per-node predicate DeadView filters on.
func (n Node) IsDead() bool {
	return !n.Reachable && !n.Generated && !n.Root
}

// IsTestOnly reports whether this node is production code kept alive only by
// tests: reachable (with tests) but not production-reachable, and itself declared
// in production, non-generated, non-root code. This is the per-node predicate
// TestOnlyView filters on.
func (n Node) IsTestOnly() bool {
	return n.Reachable && !n.ProdReachable && !n.Test && !n.Root && !n.Generated
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
	ContractVersion string `json:"contract_version"`
	Generator       string `json:"generator"`
	Language        string `json:"language"`
	Module          string `json:"module"`
	SHA             string `json:"sha"`
	Tree            string `json:"tree"`
	// Fidelity names what an edge MEANS for this backend. The values magma
	// actually emits are `"rta"` (Go) and `"semantic"` (Rust) — see the
	// vocabulary table in README.md, which is the published one.
	//
	// This comment previously read `(e.g. "rta", "syntactic")`. `"syntactic"`
	// is not a value magma has ever emitted, and it was the only place a
	// consumer could go looking for the vocabulary — so a lookup table built
	// from it carried one phantom key and was missing a real one. A downstream
	// gate hit exactly that: an unknown fidelity fell through to its weakest
	// bar, and 628 of 628 candidates from a genuine RTA call graph were
	// labelled "guess with confidence, no call graph".
	//
	// THE NAME IS OPEN, NOT A CLOSED ENUM. magma adds a language per minor
	// release and each may name its own fidelity, so a consumer must not fail
	// closed on an unrecognised value — nor silently treat it as the weakest.
	Fidelity string `json:"fidelity"`
	// ExecutedTargetCode records whether producing this graph RAN the analysed
	// repository's own code. Go never does: it type-checks only, so the Go
	// backend leaves this false. The Rust backend does — rust-analyzer executes
	// `build.rs` scripts and expands proc macros to load a workspace at all — and
	// the helper reports it per run rather than hard-coding it, so it stays
	// honest if a sandboxed mode is ever added.
	//
	// A trust-boundary fact, which is why it is carried explicitly rather than
	// left for a consumer to infer from `language == "rust"`. The helper has
	// always emitted it; until now magma parsed and dropped it.
	ExecutedTargetCode  bool   `json:"executed_target_code"`
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
		if n.IsDead() {
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
		if n.IsTestOnly() {
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

// MarshalJSON guarantees Params and Results serialize as ARRAYS, never null.
//
// The contract handshake with Architext froze this wording: "functions[].signature
// is ALWAYS present (object, never omitted): {"params":[...],"results":[...]} —
// both arrays always present, possibly empty." A nil Go slice marshals to `null`,
// so any backend that builds a Signature without initialising both fields
// silently breaks that.
//
// It happened: the Rust backend built `&Signature{}` and appended, so a function
// with no parameters emitted `"params": null`. Architext's validator rejected the
// first real Rust artifact over it — 68% of functions had null params, 50% null
// results. The Go backend had always initialised both explicitly (signatureOf),
// so the invariant lived in one backend's code rather than in the type, and the
// second backend did not inherit it.
//
// Enforced here so it cannot depend on a backend author remembering. `null` and
// `[]` are different claims — "unknown parameters" versus "no parameters" — and
// only the second is ever true of a function magma has analysed.
func (s Signature) MarshalJSON() ([]byte, error) {
	type signatureJSON Signature // distinct type: avoids recursing into this method
	out := signatureJSON(s)
	if out.Params == nil {
		out.Params = []Param{}
	}
	if out.Results == nil {
		out.Results = []Result{}
	}
	return json.Marshal(out)
}
