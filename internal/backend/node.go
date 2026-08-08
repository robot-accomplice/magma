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
	markNodeReachable(nodes, edges)
	assignFan(nodes, edges)
	g.Nodes = nodes
	g.Edges = edges
	return g, nil
}

// markNodeReachable fills Reachable and ProdReachable by BFS over the edge set.
//
// Plan 1 never called this at all: JS nodes carried Reachable=false regardless
// of the graph, which was invisible only because every node was also root=false
// and the views refused outright before reading it.
//
// Two traversals, matching the Go and Rust backends:
//
//   - ProdReachable seeds on production roots alone (Root && !Test).
//   - Reachable adds the test entry points, which the runner executes and
//     nothing in the graph calls — exactly as nothing calls `main`.
//
// THE TEST SEED IS THE NARROW SIGNAL, and the Rust backend paid for learning
// the difference. Root && Test is set only by a TEST ROOT RULE — a test file's
// module scope — never by "this function is declared in a test file", which is
// what Test alone means. Seeding on Test would make every helper in a test file
// its own root, trivially reaching itself, so a genuinely dead test helper
// could never be reported.
func markNodeReachable(nodes []contract.Node, edges []contract.Edge) {
	adj := make(map[int][]int, len(nodes))
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	var allRoots, prodRoots []int
	for _, n := range nodes {
		if !n.Root {
			continue
		}
		allRoots = append(allRoots, n.ID)
		if !n.Test {
			prodRoots = append(prodRoots, n.ID)
		}
	}
	reachAll := bfs(adj, allRoots)
	reachProd := bfs(adj, prodRoots)
	for i := range nodes {
		nodes[i].Reachable = reachAll[nodes[i].ID]
		nodes[i].ProdReachable = reachProd[nodes[i].ID]
	}
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
			// Declared from measurement, not from theory: on a real Next.js repo
			// the residual dead set was 22 of 748, and the bulk of it was
			// functions defined inside vi.mock factories. Naming the class is
			// what stops a reader treating those rows as deletion candidates.
			ID:          "js-test-double-factories",
			Scope:       contract.ScopeBackend,
			Attribution: "magma node backend",
			Description: "a module replaced by a test-double factory (vi.mock, jest.mock) is bound by the runner at run time, so functions declared inside the factory have no static caller and may report dead",
			// may-omit-edges, NOT over-approximates-live: this errs toward DEAD.
			// The runner's call into the factory is the edge that is missing.
			Effect:      contract.EffectMayOmitEdges,
			EvidencedBy: "unresolved_call_sites",
		},
		{
			// Narrowed once roots landed. The original wording said entry points
			// were not recognised AT ALL, which is no longer true and would now
			// understate the backend — a limitation left stale after the gap
			// closes is as misleading as one never declared.
			ID:          "js-roots-static-conventions-only",
			Scope:       contract.ScopeBackend,
			Attribution: "magma node backend",
			Description: "entry points are recognised from package.json (main, module, exports, bin, scripts) and a fixed table of framework path conventions; a bundler-configured entry point is not recognised, because reading one means executing the target's own JavaScript, which magma never does",
			Effect:      contract.EffectMayOmitNodes,
			EvidencedBy: "roots",
		},
	}
}
