package architext

import (
	"sort"

	"github.com/robot-accomplice/magma/internal/contract"
)

// contractVersion is the wire-format version of the code-graph artifact.
const contractVersion = "magma-code-graph/1"

// CodeGraph is the magma-code-graph/1 artifact: the objective, deterministic
// call graph in the shape Architext ingests. Fine tier: Functions + Calls.
// Coarse tier: Modules + ModuleCalls (see rollup.go).
type CodeGraph struct {
	ContractVersion     string       `json:"contract_version"`
	Generator           string       `json:"generator"`
	Language            string       `json:"language"`
	Module              string       `json:"module"`
	SHA                 string       `json:"sha"`
	Tree                string       `json:"tree"`
	Fidelity            string       `json:"fidelity"`
	Computable          bool         `json:"computable"`
	NotComputableReason string       `json:"not_computable_reason,omitempty"`
	Functions           []Function   `json:"functions"`
	Calls               []Call       `json:"calls"`
	Modules             []Module     `json:"modules"`
	ModuleCalls         []ModuleCall `json:"module_calls"`
}

// Function is one node in the fine tier.
type Function struct {
	ID            string             `json:"id"`
	Symbol        string             `json:"symbol"`
	Pkg           string             `json:"pkg"`
	File          string             `json:"file"`
	Line          int                `json:"line"`
	Kind          string             `json:"kind"`
	Exported      bool               `json:"exported"`
	Test          bool               `json:"test"`
	Root          bool               `json:"root"`
	Generated     bool               `json:"generated"`
	Reachable     bool               `json:"reachable"`
	ProdReachable bool               `json:"prod_reachable"`
	Signature     contract.Signature `json:"signature"`
	Doc           string             `json:"doc,omitempty"`
	FanIn         int                `json:"fan_in"`
	FanOut        int                `json:"fan_out"`
}

// Call is one edge in the fine tier; endpoints are function ids (slugs).
type Call struct {
	From     string `json:"from"`
	To       string `json:"to"`
	SiteFile string `json:"site_file"`
	SiteLine int    `json:"site_line"`
	Kind     string `json:"kind"`
}

// Emit maps a computed graph to the code-graph artifact. A refused graph yields
// a refused code-graph (computable=false, nil slices) — never a fabricated one.
func Emit(g contract.Graph) CodeGraph {
	cg := CodeGraph{
		ContractVersion: contractVersion, Generator: g.Generator,
		Language: g.Language, Module: g.Module, SHA: g.SHA, Tree: g.Tree,
		Fidelity: g.Fidelity, Computable: g.Computable, NotComputableReason: g.NotComputableReason,
	}
	if !g.Computable {
		return cg // nil Functions/Calls/Modules/ModuleCalls
	}
	ids := assignIDs(g.Nodes)
	for _, n := range g.Nodes {
		sig := contract.Signature{Params: []contract.Param{}, Results: []contract.Result{}}
		if n.Signature != nil {
			sig = *n.Signature
		}
		cg.Functions = append(cg.Functions, Function{
			ID: ids[n.ID], Symbol: n.Symbol, Pkg: n.Pkg, File: n.File, Line: n.Line,
			Kind: n.Kind, Exported: n.Exported, Test: n.Test, Root: n.Root, Generated: n.Generated,
			Reachable: n.Reachable, ProdReachable: n.ProdReachable,
			Signature: sig, Doc: n.Doc, FanIn: n.FanIn, FanOut: n.FanOut,
		})
	}
	for _, e := range g.Edges {
		cg.Calls = append(cg.Calls, Call{
			From: ids[e.From], To: ids[e.To], SiteFile: e.File, SiteLine: e.Line, Kind: e.Kind,
		})
	}
	cg.Modules, cg.ModuleCalls = rollup(g.Nodes, g.Edges, ids)
	sortCodeGraph(&cg)
	return cg
}

// sortCodeGraph enforces byte-determinism: every slice is ordered by a stable key.
func sortCodeGraph(cg *CodeGraph) {
	sort.Slice(cg.Functions, func(i, j int) bool { return cg.Functions[i].ID < cg.Functions[j].ID })
	sort.Slice(cg.Calls, func(i, j int) bool {
		if cg.Calls[i].From != cg.Calls[j].From {
			return cg.Calls[i].From < cg.Calls[j].From
		}
		return cg.Calls[i].To < cg.Calls[j].To
	})
	sort.Slice(cg.Modules, func(i, j int) bool { return cg.Modules[i].ID < cg.Modules[j].ID })
	sort.Slice(cg.ModuleCalls, func(i, j int) bool {
		if cg.ModuleCalls[i].From != cg.ModuleCalls[j].From {
			return cg.ModuleCalls[i].From < cg.ModuleCalls[j].From
		}
		return cg.ModuleCalls[i].To < cg.ModuleCalls[j].To
	})
}
