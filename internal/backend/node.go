package backend

import (
	"path/filepath"

	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
)

func init() { register(detect.Node, nodeBackend{}) }

// nodeBackend maps a JavaScript or TypeScript project by driving the vendored
// TypeScript compiler under the target's own node.
//
// UNLIKE THE RUST BACKEND, NOTHING NEEDS INSTALLING. The compiler is embedded
// in the magma binary and extracted on first use, deliberately correcting the
// Rust helper's gap: that binary is in neither the release archive nor
// crates.io, so `go install` cannot analyse Rust at all. Here, magma plus node
// is the whole requirement.
//
// fidelity = "semantic": edges come from TypeScript's own name resolution and
// type inference, so a resolved call lands on the declaration the compiler
// would select — over plain .js as well as .ts, since the checker is a
// JavaScript analyser with an optional type layer rather than a TS analyser
// tolerating JS. Call sites with no static target are COUNTED, never silently
// dropped: unresolvable is a fact about JavaScript, uncounted is a defect.
type nodeBackend struct {
	// engine is nil in production and set by tests. The indirection is what
	// makes an in-process TypeScript 7 an addition rather than a rewrite.
	engine jsEngine
}

func (nodeBackend) Language() string { return "node" }

func (b nodeBackend) BuildGraph(repo string, meta contract.Meta, progress Progress) (contract.Graph, error) {
	step := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	g := contract.NewGraph(meta, "node", "semantic")
	// Set before any refusal path below: a refusal is exactly when a consumer
	// most needs to know what this backend cannot do.
	g.Limitations = nodeLimitations()

	// JavaScript has no single identifier analogous to a go.mod module path — a
	// repo is a set of packages — so the directory name labels the map. Package
	// identity is not lost: every node carries its directory in `pkg`.
	g.Module = filepath.Base(filepath.Clean(repo))

	e := b.engine
	if e == nil {
		e = nodeEngine{}
	}
	env, err := e.Analyse(repo, step)
	if err != nil {
		// A missing runtime is a REFUSAL, not an error. A repo magma cannot map
		// honestly gets an honest refusal — the same treatment an unsupported
		// language gets, so the audit gate sees a reason rather than a crash.
		return g.Refuse(err.Error()), nil
	}
	// Computable is present only on a refusal envelope, so a nil pointer means
	// the analysis succeeded. Distinguishing on presence rather than on
	// `functions == nil` keeps an empty-but-real repo from reading as a refusal.
	if env.Computable != nil && !*env.Computable {
		return g.Refuse(env.Reason), nil
	}

	nodes := make([]contract.Node, 0, len(env.Functions))
	for _, f := range env.Functions {
		nodes = append(nodes, contract.Node{
			ID: f.ID, Symbol: f.Symbol, Pkg: f.Pkg, File: f.File, Line: f.Line,
			Kind: f.Kind, Exported: f.Exported, Test: f.Test,
			Root: f.Root, Generated: f.Generated,
		})
	}
	edges := make([]contract.Edge, 0, len(env.Calls))
	for _, c := range env.Calls {
		edges = append(edges, contract.Edge{
			From: c.From, To: c.To, File: c.SiteFile, Line: c.SiteLine, Kind: c.Kind,
		})
	}

	// The driver's own answer, carried through rather than assumed: this
	// backend type-checks and never runs the target's code, unlike Rust's,
	// which must execute build scripts to load a workspace at all.
	g.ExecutedTargetCode = env.ExecutedTargetCode

	step("analyzing reachability")
	assignFan(nodes, edges)
	g.Nodes = nodes
	g.Edges = edges
	return g, nil
}

// nodeLimitations declares what this backend cannot do.
//
// Declared from the FIRST commit rather than added once someone notices. A
// backend that declares none reads as unlimited, which is precisely the
// "unlimited by omission" failure the field exists to prevent — and a brand-new
// backend is where that omission is easiest to make.
func nodeLimitations() contract.Limitations {
	return contract.Limitations{
		{
			ID:          "js-dynamic-call-sites",
			Scope:       contract.ScopeLanguage,
			Attribution: "JavaScript",
			Description: "a computed member call, a dynamic import(), or require() with a variable specifier has no static target, so no edge can be resolved for it",
			Effect:      contract.EffectMayOmitEdges,
			EvidencedBy: "unresolved_call_sites",
		},
		{
			ID:          "js-roots-not-yet-framework-aware",
			Scope:       contract.ScopeBackend,
			Attribution: "magma node backend",
			Description: "package exports, framework file-system routes and script targets are not yet recognised as entry points, so every node reports root:false and the reachability views under-report liveness",
			Effect:      contract.EffectMayOmitNodes,
			EvidencedBy: "roots",
		},
	}
}
