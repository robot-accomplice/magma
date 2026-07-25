package backend

import (
	"fmt"
	"go/ast"
	"go/doc"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
)

func init() { register(detect.Go, goBackend{}) }

// goBackend builds a type-precise call graph for a Go module using the same
// x/tools machinery `deadcode` uses (packages + ssa + rta), but it KEEPS the
// call graph instead of discarding everything but the unreachable leaves. It is
// entirely in-process: the only external requirement is a working `go`.
//
// fidelity = "rta": edges are Rapid Type Analysis edges. Static calls are exact;
// dynamic (interface / func-value) calls are the RTA over-approximation. Calls
// routed through closures or synthetic wrappers are not yet represented as node
// edges (v1 emits the direct source-declaration graph).
type goBackend struct{}

func (goBackend) Language() string { return "go" }

func (goBackend) BuildGraph(repo string, meta contract.Meta, progress Progress) (contract.Graph, error) {
	step := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	g := contract.NewGraph(meta, "go", "rta")

	// Two loads, deliberately — the same design deadcode uses. A single Tests:true
	// load creates duplicate SSA variants of each package ("p" and "p [p.test]"),
	// and production reachability computed over that mixed program badly
	// under-reaches. So: load WITH tests for the emitted graph and all-roots
	// reachability, load WITHOUT tests for production reachability. Positions are
	// file:line:col, identical across loads, so the two reachability sets compose.
	step("loading packages (with tests)")
	withTests, reason, err := load(repo, true)
	if err != nil {
		return g, err
	}
	if reason != "" {
		return g.Refuse(reason), nil
	}
	if len(withTests.mains) == 0 {
		// With Tests:true even a pure library yields a synthetic test main, so
		// zero mains means the scope has no entrypoint at all.
		return g.Refuse("no entrypoint (main or test) in scope; reachability not computable"), nil
	}

	step("analyzing reachability")
	var allRoots []*ssa.Function
	for _, m := range withTests.mains {
		allRoots = appendRoots(allRoots, m)
	}
	resAll := rta.Analyze(allRoots, true)
	reachAll := reachablePositions(withTests.prog, resAll)

	// Production reachability from the tests-excluded program. An empty result
	// (no production main) is honest: ProdReachable stays all-false and the
	// derived views refuse via hasProdRoot.
	step("loading packages (production)")
	reachProd := map[token.Position]bool{}
	prod, prodReason, err := load(repo, false)
	if err != nil {
		return g, err
	}
	if prodReason == "" {
		var prodRoots []*ssa.Function
		for _, m := range prod.mains {
			prodRoots = appendRoots(prodRoots, m)
		}
		if len(prodRoots) > 0 {
			step("analyzing production reachability")
			reachProd = reachablePositions(prod.prog, rta.Analyze(prodRoots, false))
		}
	}

	// The map is about THIS repo, not its dependency closure: restrict nodes to
	// the module's own packages (the same filter deadcode applies to its output).
	step("collecting nodes")
	modPath := moduledPath(withTests.initial)
	g.Module = modPath
	nodes, posnID := collectNodes(repo, modPath, withTests.prog, withTests.initial, reachAll, reachProd, withTests.mains)
	step("collecting edges")
	edges := collectEdges(repo, withTests.prog, resAll.CallGraph, posnID)
	assignFan(nodes, edges)

	g.Nodes = nodes
	g.Edges = edges
	return g, nil
}

// loaded is one type-checked, SSA-built program.
type loaded struct {
	initial []*packages.Package
	prog    *ssa.Program
	mains   []*ssa.Package
}

// load type-checks and SSA-builds the repo. reason (non-empty) is a soft refusal
// the caller turns into a refused graph; err is a hard failure.
func load(repo string, tests bool) (*loaded, string, error) {
	cfg := &packages.Config{
		Mode:  packages.LoadAllSyntax | packages.NeedModule,
		Tests: tests,
		Dir:   repo,
		// Wall-2 hardening: pin the toolchain and forbid go.mod/go.sum mutation
		// or a network toolchain fetch while we load the target.
		Env: append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly"),
	}
	initial, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, "", fmt.Errorf("packages.Load failed: %w", err)
	}
	if len(initial) == 0 {
		return nil, "no Go packages found in scope", nil
	}
	// A graph built over packages that don't type-check is untrustworthy; refuse
	// rather than emit a partial graph that reads as authoritative.
	if n := packages.PrintErrors(initial); n > 0 {
		return nil, fmt.Sprintf("%d package(s) contain type/load errors; graph not computable", n), nil
	}
	prog, pkgs := ssautil.AllPackages(initial, ssa.InstantiateGenerics)
	prog.Build()
	return &loaded{initial: initial, prog: prog, mains: ssautil.MainPackages(pkgs)}, "", nil
}

// appendRoots adds a main package's init and main functions as call-graph roots.
func appendRoots(roots []*ssa.Function, m *ssa.Package) []*ssa.Function {
	for _, name := range []string{"init", "main"} {
		if fn := m.Func(name); fn != nil {
			roots = append(roots, fn)
		}
	}
	return roots
}

// reachablePositions maps a Reachable set to declaration positions. A source
// function is live if ANY of its SSA variants (e.g. p vs p[p.test]) is reachable,
// so we dedup by position, exactly as deadcode does.
func reachablePositions(prog *ssa.Program, res *rta.Result) map[token.Position]bool {
	m := make(map[token.Position]bool)
	for fn := range res.Reachable {
		if fn.Pos().IsValid() || fn.Name() == "init" {
			m[prog.Fset.Position(fn.Pos())] = true
		}
	}
	return m
}

// collectNodes builds one node per source function declaration, deduped by
// position, and returns the position→id map used to resolve edges.
func collectNodes(
	repo, modPath string,
	prog *ssa.Program,
	initial []*packages.Package,
	reachAll, reachProd map[token.Position]bool,
	mains []*ssa.Package,
) ([]contract.Node, map[token.Position]int) {
	mainPkgs := make(map[*types.Package]bool)
	for _, m := range mains {
		mainPkgs[m.Pkg] = true
	}

	generated := make(map[string]bool)
	type decl struct {
		fn  *ssa.Function
		obj *types.Func
		doc string
	}
	var decls []decl
	seen := make(map[token.Position]bool)

	packages.Visit(initial, nil, func(p *packages.Package) {
		if !inModule(p.PkgPath, modPath) {
			return // dependency / stdlib package: not part of this repo's map
		}
		for _, file := range p.Syntax {
			if ast.IsGenerated(file) {
				generated[p.Fset.File(file.Pos()).Name()] = true
			}
			for _, d := range file.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				obj, _ := p.TypesInfo.Defs[fd.Name].(*types.Func)
				if obj == nil {
					continue
				}
				fn := prog.FuncValue(obj)
				if fn == nil {
					continue
				}
				posn := prog.Fset.Position(fn.Pos())
				if seen[posn] {
					continue // a test variant of an already-seen declaration
				}
				seen[posn] = true
				decls = append(decls, decl{fn: fn, obj: obj, doc: synopsis(fd.Doc)})
			}
		}
	})

	nodes := make([]contract.Node, 0, len(decls))
	posnID := make(map[token.Position]int, len(decls))
	for i, d := range decls {
		posn := prog.Fset.Position(d.fn.Pos())
		kind := "func"
		if d.fn.Signature.Recv() != nil {
			kind = "method"
		}
		isMain := d.fn.Name() == "main" && d.fn.Pkg != nil && mainPkgs[d.fn.Pkg.Pkg]
		nodes = append(nodes, contract.Node{
			ID:            i,
			Symbol:        prettyName(d.fn),
			Pkg:           pkgPath(d.fn),
			File:          rel(repo, posn.Filename),
			Line:          posn.Line,
			Kind:          kind,
			Exported:      d.obj.Exported(),
			Test:          strings.HasSuffix(posn.Filename, "_test.go"),
			Root:          isMain,
			Generated:     generated[posn.Filename],
			Reachable:     reachAll[posn],
			ProdReachable: reachProd[posn],
			Signature:     signatureOf(d.fn.Signature),
			Doc:           d.doc,
		})
		posnID[posn] = i
	}
	return nodes, posnID
}

// collectEdges walks the RTA call graph and emits one edge per distinct
// (caller, callee) pair where both endpoints are source declarations we mapped.
// A pair is "static" if any of its call sites is a resolved static call.
func collectEdges(repo string, prog *ssa.Program, cg *callgraph.Graph, posnID map[token.Position]int) []contract.Edge {
	type key struct{ from, to int }
	agg := make(map[key]*contract.Edge)

	for fn, node := range cg.Nodes {
		if fn == nil {
			continue
		}
		fromID, ok := posnID[prog.Fset.Position(fn.Pos())]
		if !ok {
			continue
		}
		for _, e := range node.Out {
			if e.Callee == nil || e.Callee.Func == nil {
				continue
			}
			toID, ok := posnID[prog.Fset.Position(e.Callee.Func.Pos())]
			if !ok {
				continue
			}
			k := key{fromID, toID}
			static := e.Site != nil && e.Site.Common().StaticCallee() != nil
			cur, exists := agg[k]
			if !exists {
				site := token.Position{}
				if e.Site != nil {
					site = prog.Fset.Position(e.Site.Pos())
				}
				kind := "dynamic"
				if static {
					kind = "static"
				}
				agg[k] = &contract.Edge{From: fromID, To: toID, File: rel(repo, site.Filename), Line: site.Line, Kind: kind}
				continue
			}
			if static && cur.Kind != "static" {
				cur.Kind = "static" // a static site upgrades the pair
			}
		}
	}

	edges := make([]contract.Edge, 0, len(agg))
	for _, e := range agg {
		edges = append(edges, *e)
	}
	return edges
}

// assignFan sets each node's FanIn/FanOut from the deduped edge set: FanOut is
// the number of distinct callees, FanIn the number of distinct callers. Edges
// are already deduped per (from,to) by collectEdges, so a plain tally suffices.
func assignFan(nodes []contract.Node, edges []contract.Edge) {
	in := make(map[int]int, len(nodes))
	out := make(map[int]int, len(nodes))
	for _, e := range edges {
		out[e.From]++
		in[e.To]++
	}
	for i := range nodes {
		nodes[i].FanIn = in[nodes[i].ID]
		nodes[i].FanOut = out[nodes[i].ID]
	}
}

// prettyName renders a function/method name without go/ssa's punctuation, e.g.
// "(*pkg.T).F" -> "T.F". It is a self-contained fork of deadcode's helper that
// avoids the x/tools-internal receiver helper.
func prettyName(fn *ssa.Function) string {
	name := fn.Name()
	if recv := fn.Signature.Recv(); recv != nil {
		t := recv.Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			return named.Obj().Name() + "." + name
		}
	}
	return name
}

// synopsis returns the first sentence of a declaration's doc comment, or "" when
// there is none (a nil CommentGroup yields empty text). It calls the method on a
// zero-value doc.Package rather than the package-level doc.Synopsis, which is
// deprecated as of Go 1.20: that function is itself defined as this exact call, so
// the result is unchanged. A doc.Package built from real files would additionally
// resolve doc links, which does not apply here — the synopsis is stored as plain
// metadata, never rendered as Go doc.
func synopsis(cg *ast.CommentGroup) string {
	var pkg doc.Package
	return pkg.Synopsis(cg.Text())
}

// typeQualifier renders package-qualified types as "pkg.Name" (short package
// name), matching prettyName's receiver rendering — never the full import path.
func typeQualifier(p *types.Package) string { return p.Name() }

// signatureOf renders a types.Signature into the contract's Param/Result form.
// Parameter names are kept; result names are dropped (the type is the signal).
func signatureOf(sig *types.Signature) *contract.Signature {
	out := &contract.Signature{Params: []contract.Param{}, Results: []contract.Result{}}
	if params := sig.Params(); params != nil {
		for i := 0; i < params.Len(); i++ {
			v := params.At(i)
			out.Params = append(out.Params, contract.Param{
				Name: v.Name(),
				Type: types.TypeString(v.Type(), typeQualifier),
			})
		}
	}
	if results := sig.Results(); results != nil {
		for i := 0; i < results.Len(); i++ {
			out.Results = append(out.Results, contract.Result{
				Type: types.TypeString(results.At(i).Type(), typeQualifier),
			})
		}
	}
	return out
}

// moduledPath returns the main module's import path from the loaded packages,
// or "" when the scope has no module (then inModule matches everything).
func moduledPath(initial []*packages.Package) string {
	for _, p := range initial {
		if p.Module != nil && p.Module.Path != "" {
			return p.Module.Path
		}
	}
	return ""
}

// inModule reports whether an import path belongs to the module rooted at
// modPath — the path itself or anything beneath it. An empty modPath (no module)
// matches everything, so a GOPATH-style repo still maps.
func inModule(pkgPath, modPath string) bool {
	if modPath == "" {
		return true
	}
	return pkgPath == modPath || strings.HasPrefix(pkgPath, modPath+"/")
}

func pkgPath(fn *ssa.Function) string {
	if fn.Pkg != nil && fn.Pkg.Pkg != nil {
		return fn.Pkg.Pkg.Path()
	}
	return ""
}

// rel makes an absolute analyzer path repo-relative; it falls back to the
// original path if that isn't possible.
func rel(repo, path string) string {
	if path == "" {
		return ""
	}
	if r, err := filepath.Rel(repo, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}
