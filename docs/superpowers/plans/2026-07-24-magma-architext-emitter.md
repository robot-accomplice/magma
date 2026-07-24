# Magma → Architext code-graph emitter — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich magma's call graph with per-function signature/doc/fan data and emit a deterministic `magma-code-graph/1` artifact, dual-written to the target repo's `docs/architext/data/` and the vault `.magma/` mirror, so Architext can ingest an objective view of the code.

**Architecture:** Add signature/doc/fan fields to the domain `contract.Node`; extract them in the Go backend from the SSA `types.Signature` already in hand. A new outer-ring adapter package `internal/architext/` maps the in-memory `contract.Graph` (fine + coarse tiers) to the JSON contract and writes it. Magma's core (`contract`, `backend`) never imports the adapter — the dependency arrow points inward only.

**Tech Stack:** Go 1.26, `golang.org/x/tools` (go/packages, ssa, callgraph/rta — already vendored), stdlib `go/doc`, `go/types`, `encoding/json`. No new third-party dependencies (magma decision #3).

## Global Constraints

- **Determinism:** same repo + SHA → byte-identical `code-graph.json` (sorted arrays, stable ids). Copy verbatim from spec.
- **No wall-clock time in the JSON artifact.** Any timestamp is a viewer concern (Spec B), never here.
- **Signature enrichment is Go-only.** Other backends refuse until their parser exists (magma decision #5).
- **Honest refusal:** an uncomputable graph yields a refused `code-graph.json` (`computable:false` + reason, null slices), never a fabricated one.
- **Contract version string:** `magma-code-graph/1` (a named constant, defined once).
- **No new external dependencies.** Only the Go toolchain + already-present `x/tools`.
- **80% LOC coverage bar** held; `go test ./...`, `go vet ./...`, `gofmt -l` all clean.
- **Default architext destination:** the target repo's `docs/architext/data/` (a named constant).

---

### Task 1: Domain types — signature/doc/fan on `contract.Node` + shared reachability predicates

**Files:**
- Modify: `internal/contract/graph.go` (Node struct ~line 12-33; DeadView ~line 105-119; TestOnlyView ~line 122-145)
- Test: `internal/contract/graph_test.go`

**Interfaces:**
- Produces: types `Param{Name,Type string}`, `Result{Type string}`, `Signature{Params []Param, Results []Result}`; `Node` fields `Signature *Signature`, `Doc string`, `FanIn int`, `FanOut int`; methods `func (n Node) IsDead() bool`, `func (n Node) IsTestOnly() bool`.

- [ ] **Step 1: Write the failing test**

```go
// internal/contract/graph_test.go
package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNodeSignatureMarshals(t *testing.T) {
	n := Node{
		ID: 0, Symbol: "Add", Pkg: "sigmod", Kind: "func",
		Signature: &Signature{
			Params:  []Param{{Name: "a", Type: "int"}, {Name: "b", Type: "int"}},
			Results: []Result{{Type: "int"}},
		},
		Doc: "Add returns the sum of a and b.", FanIn: 1, FanOut: 0,
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"signature"`, `"a"`, `"fan_in":1`, `"doc":"Add returns the sum of a and b."`} {
		if !strings.Contains(got, want) {
			t.Errorf("marshalled node missing %s\ngot: %s", want, got)
		}
	}
}

func TestReachabilityPredicates(t *testing.T) {
	dead := Node{Reachable: false, Generated: false, Root: false}
	if !dead.IsDead() {
		t.Error("unreachable, non-generated, non-root node should be dead")
	}
	if Node{Reachable: false, Root: true}.IsDead() {
		t.Error("a root is reachable by definition; never dead")
	}
	testOnly := Node{Reachable: true, ProdReachable: false, Test: false, Root: false, Generated: false}
	if !testOnly.IsTestOnly() {
		t.Error("reachable-but-not-prod, non-test production node should be test-only")
	}
	if (Node{Reachable: true, ProdReachable: false, Test: true}).IsTestOnly() {
		t.Error("a function declared in a test file is test code, not test-only production code")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/code/magma && go test ./internal/contract/ -run 'TestNodeSignature|TestReachabilityPredicates' -v`
Expected: FAIL — `Signature`/`Param`/`Result` undefined, `IsDead`/`IsTestOnly` undefined.

- [ ] **Step 3: Add the types + fields**

In `internal/contract/graph.go`, add above `type Node struct`:

```go
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
```

Add these fields to `Node` (after `ProdReachable`):

```go
	// Signature is the function's parameters and results. Nil for backends that
	// do not (yet) extract signatures. Set for every Go node.
	Signature *Signature `json:"signature,omitempty"`
	// Doc is the first sentence of the declaration's doc comment ("" if none).
	Doc string `json:"doc,omitempty"`
	// FanIn / FanOut are the distinct in/out call-edge counts, derived from the graph.
	FanIn  int `json:"fan_in"`
	FanOut int `json:"fan_out"`
```

- [ ] **Step 4: Add the predicates + refactor the views to use them**

Add to `internal/contract/graph.go`:

```go
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
```

Replace the loop condition in `DeadView` — change
`if !n.Reachable && !n.Generated && !n.Root {` to `if n.IsDead() {`.
Replace the loop condition in `TestOnlyView` — change
`if n.Reachable && !n.ProdReachable && !n.Test && !n.Root && !n.Generated {` to `if n.IsTestOnly() {`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd ~/code/magma && go test ./internal/contract/ -v`
Expected: PASS (new tests + all existing view tests still green — the refactor is behavior-preserving).

- [ ] **Step 6: Commit**

```bash
git add internal/contract/graph.go internal/contract/graph_test.go
git commit -m "feat(contract): signature/doc/fan node fields + shared IsDead/IsTestOnly predicates"
```

---

### Task 2: Extract signature + doc in the Go backend

**Files:**
- Modify: `internal/backend/golang.go` (`decl` struct ~line 180-183; collection loop ~line 195-214; node-build loop ~line 220-242; add helpers near `prettyName` ~line 300)
- Create: `internal/backend/testdata/sigmod/go.mod`, `internal/backend/testdata/sigmod/main.go`
- Test: `internal/backend/golang_test.go` (add cases; file already exists)

**Interfaces:**
- Consumes: `contract.Signature`, `contract.Param`, `contract.Result`, `Node.Signature`, `Node.Doc` (Task 1).
- Produces: every Go node carries a non-nil `Signature` and a `Doc` (first sentence).

- [ ] **Step 1: Create the signature fixture**

`internal/backend/testdata/sigmod/go.mod`:

```
module sigmod

go 1.26
```

`internal/backend/testdata/sigmod/main.go`:

```go
package main

// Add returns the sum of a and b.
func Add(a int, b int) int { return a + b }

// sq returns n squared.
func sq(n int) int { return n * n }

func main() {
	_ = Add(sq(2), sq(3))
}
```

- [ ] **Step 2: Write the failing test**

Add to `internal/backend/golang_test.go`:

```go
func TestBuildGraphExtractsSignatureAndDoc(t *testing.T) {
	g, err := BuildGraph("testdata/sigmod")
	if err != nil {
		t.Fatal(err)
	}
	var add *contract.Node
	for i := range g.Nodes {
		if g.Nodes[i].Symbol == "Add" {
			add = &g.Nodes[i]
		}
	}
	if add == nil {
		t.Fatal("no node for Add")
	}
	if add.Signature == nil {
		t.Fatal("Add has no signature")
	}
	wantParams := []contract.Param{{Name: "a", Type: "int"}, {Name: "b", Type: "int"}}
	if !reflect.DeepEqual(add.Signature.Params, wantParams) {
		t.Errorf("params = %+v, want %+v", add.Signature.Params, wantParams)
	}
	if len(add.Signature.Results) != 1 || add.Signature.Results[0].Type != "int" {
		t.Errorf("results = %+v, want [{int}]", add.Signature.Results)
	}
	if add.Doc != "Add returns the sum of a and b." {
		t.Errorf("doc = %q, want the first sentence of the comment", add.Doc)
	}
}
```

Ensure the test file imports `reflect` and `github.com/robot-accomplice/magma/internal/contract` (add if absent).

- [ ] **Step 3: Run test to verify it fails**

Run: `cd ~/code/magma && go test ./internal/backend/ -run TestBuildGraphExtractsSignatureAndDoc -v`
Expected: FAIL — `add.Signature` is nil.

- [ ] **Step 4: Implement extraction**

In `golang.go`, extend the `decl` struct (~line 180) to carry the doc text:

```go
	type decl struct {
		fn  *ssa.Function
		obj *types.Func
		doc string
	}
```

In the collection loop, where `decls = append(decls, decl{fn, obj})` (~line 213), capture the doc synopsis (`fd.Doc` is nil-safe; `doc.Synopsis` returns the first sentence):

```go
			decls = append(decls, decl{fn: fn, obj: obj, doc: doc.Synopsis(fd.Doc.Text())})
```

In the node-build loop (~line 227), set the two new fields on the `contract.Node`:

```go
			Signature: signatureOf(d.fn.Signature),
			Doc:       d.doc,
```

Add helpers near `prettyName` (~line 313):

```go
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
```

Add `"go/doc"` to the import block.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd ~/code/magma && go test ./internal/backend/ -v`
Expected: PASS (new test + existing `livemod`/`libmod` tests unchanged — `sigmod` is a separate fixture, so exact-set assertions are untouched).

- [ ] **Step 6: Commit**

```bash
git add internal/backend/golang.go internal/backend/golang_test.go internal/backend/testdata/sigmod
git commit -m "feat(backend): extract per-function signature and doc synopsis (Go)"
```

---

### Task 3: Compute fan-in / fan-out in the backend

**Files:**
- Modify: `internal/backend/golang.go` (`BuildGraph`, right after `edges := collectEdges(...)` ~line 100; add helper near `collectEdges` ~line 295)
- Test: `internal/backend/golang_test.go`

**Interfaces:**
- Consumes: `Node.FanIn`, `Node.FanOut` (Task 1); `collectEdges` output.
- Produces: every node's `FanIn`/`FanOut` set to distinct in/out edge counts.

- [ ] **Step 1: Write the failing test**

Add to `internal/backend/golang_test.go`:

```go
func TestBuildGraphComputesFan(t *testing.T) {
	g, err := BuildGraph("testdata/sigmod")
	if err != nil {
		t.Fatal(err)
	}
	fan := map[string][2]int{} // symbol -> {fanIn, fanOut}
	for _, n := range g.Nodes {
		fan[n.Symbol] = [2]int{n.FanIn, n.FanOut}
	}
	// main calls Add and sq (sq twice, deduped to one edge) -> fanOut 2, fanIn 0.
	if fan["main"] != [2]int{0, 2} {
		t.Errorf("main fan = %v, want {0,2}", fan["main"])
	}
	// Add is called once by main, calls nothing -> fanIn 1, fanOut 0.
	if fan["Add"] != [2]int{1, 0} {
		t.Errorf("Add fan = %v, want {1,0}", fan["Add"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/code/magma && go test ./internal/backend/ -run TestBuildGraphComputesFan -v`
Expected: FAIL — fan is `{0,0}` for every node.

- [ ] **Step 3: Implement fan assignment**

In `BuildGraph`, immediately after `edges := collectEdges(...)`, add:

```go
	assignFan(nodes, edges)
```

Add the helper near `collectEdges` (~line 295):

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/code/magma && go test ./internal/backend/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/golang.go internal/backend/golang_test.go
git commit -m "feat(backend): compute per-node fan-in/fan-out from the edge set"
```

---

### Task 4: `internal/architext` — deterministic ids

**Files:**
- Create: `internal/architext/ids.go`
- Test: `internal/architext/ids_test.go`

**Interfaces:**
- Consumes: `contract.Node` (fields `ID`, `Pkg`, `Symbol`, `File`, `Line`).
- Produces: `func moduleID(pkg string) string`; `func assignIDs(nodes []contract.Node) map[int]string` (node.ID → stable slug, collisions disambiguated).

- [ ] **Step 1: Write the failing test**

`internal/architext/ids_test.go`:

```go
package architext

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestAssignIDsStableAndSlugged(t *testing.T) {
	nodes := []contract.Node{
		{ID: 0, Pkg: "internal/backend", Symbol: "BuildGraph", File: "internal/backend/golang.go", Line: 44},
	}
	got := assignIDs(nodes)
	if got[0] != "internal-backend-buildgraph" {
		t.Errorf("id = %q, want internal-backend-buildgraph", got[0])
	}
	// Determinism: same input, same output.
	if again := assignIDs(nodes); again[0] != got[0] {
		t.Error("assignIDs is not deterministic")
	}
}

func TestAssignIDsDisambiguatesCollisions(t *testing.T) {
	// A value method T.M and a pointer method T.M in the same package slug the
	// same; disambiguate deterministically by (file, line) order.
	nodes := []contract.Node{
		{ID: 0, Pkg: "p", Symbol: "T.M", File: "b.go", Line: 20},
		{ID: 1, Pkg: "p", Symbol: "T.M", File: "a.go", Line: 10},
	}
	got := assignIDs(nodes)
	if got[1] != "p-t-m" || got[0] != "p-t-m-2" {
		t.Errorf("collision resolution = %v, want id1=p-t-m (a.go:10 first), id0=p-t-m-2", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/code/magma && go test ./internal/architext/ -v`
Expected: FAIL — package/functions do not exist yet.

- [ ] **Step 3: Implement ids.go**

```go
// Package architext maps magma's in-memory call graph to the machine-authored
// magma-code-graph/1 artifact Architext ingests. It is an outer-ring output
// adapter: it depends on internal/contract; nothing in contract or the backends
// depends on it. The dependency arrow points inward only.
package architext

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/robot-accomplice/magma/internal/contract"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug lowercases s and collapses every run of non-[a-z0-9] into a single "-",
// trimming leading/trailing dashes. Deterministic and dependency-free.
func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// moduleID is the stable slug for a package.
func moduleID(pkg string) string { return slug(pkg) }

// assignIDs returns a stable slug for every node ID, keyed by node.ID. The base
// slug is slug(pkg + "-" + symbol). Collisions (e.g. value + pointer methods on
// one type) are broken deterministically by (file, line, id): the node earliest
// by that key keeps the base slug; the next gets "-2", then "-3", and so on. The
// trailing node.ID makes the order TOTAL (ids are unique, assigned upstream in
// collectNodes), so the result is input-order-independent even when two colliding
// nodes share a file and line — (file, line) alone is not a total order.
func assignIDs(nodes []contract.Node) map[int]string {
	byBase := map[string][]contract.Node{}
	for _, n := range nodes {
		base := slug(n.Pkg + "-" + n.Symbol)
		byBase[base] = append(byBase[base], n)
	}
	out := make(map[int]string, len(nodes))
	for base, group := range byBase {
		sort.Slice(group, func(i, j int) bool {
			if group[i].File != group[j].File {
				return group[i].File < group[j].File
			}
			if group[i].Line != group[j].Line {
				return group[i].Line < group[j].Line
			}
			return group[i].ID < group[j].ID
		})
		for i, n := range group {
			if i == 0 {
				out[n.ID] = base
			} else {
				out[n.ID] = base + "-" + strconv.Itoa(i+1)
			}
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/code/magma && go test ./internal/architext/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/architext/ids.go internal/architext/ids_test.go
git commit -m "feat(architext): deterministic node/module id slugs with collision resolution"
```

---

### Task 5: `internal/architext` — `Emit` fine tier + DTOs + refusal

**Files:**
- Create: `internal/architext/emit.go`
- Test: `internal/architext/emit_test.go`

**Interfaces:**
- Consumes: `contract.Graph`, `contract.Node`, `contract.Edge`; `assignIDs` (Task 4).
- Produces: DTO types `CodeGraph`, `Function`, `Call`, `Module`, `ModuleCounts`, `ModuleCall`; `func Emit(g contract.Graph) CodeGraph`. In this task `Emit` fills the fine tier (`Functions`, `Calls`) and leaves `Modules`/`ModuleCalls` nil (Task 6 fills them). The `contractVersion` const lives here.

- [ ] **Step 1: Write the failing test**

`internal/architext/emit_test.go`:

```go
package architext

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func sampleGraph() contract.Graph {
	return contract.Graph{
		ContractVersion: contract.GraphVersion, Generator: "magma/test",
		Language: "go", Module: "m", SHA: "abc", Tree: "clean",
		Fidelity: "rta", Computable: true,
		Nodes: []contract.Node{
			{ID: 0, Symbol: "main", Pkg: "m", File: "main.go", Line: 7, Kind: "func", Root: true, Reachable: true, ProdReachable: true, FanOut: 1, Signature: &contract.Signature{Params: []contract.Param{}, Results: []contract.Result{}}},
			{ID: 1, Symbol: "Add", Pkg: "m", File: "main.go", Line: 3, Kind: "func", Exported: true, Reachable: true, ProdReachable: true, FanIn: 1, Signature: &contract.Signature{Params: []contract.Param{{Name: "a", Type: "int"}}, Results: []contract.Result{{Type: "int"}}}},
		},
		Edges: []contract.Edge{{From: 0, To: 1, File: "main.go", Line: 7, Kind: "static"}},
	}
}

func TestEmitFineTier(t *testing.T) {
	cg := Emit(sampleGraph())
	if cg.ContractVersion != "magma-code-graph/1" {
		t.Errorf("contract_version = %q", cg.ContractVersion)
	}
	if len(cg.Functions) != 2 || len(cg.Calls) != 1 {
		t.Fatalf("functions=%d calls=%d, want 2 and 1", len(cg.Functions), len(cg.Calls))
	}
	// Functions are sorted by id; "add" < "m-main"? ids are m-main and m-add.
	byID := map[string]Function{}
	for _, f := range cg.Functions {
		byID[f.ID] = f
	}
	add, ok := byID["m-add"]
	if !ok {
		t.Fatalf("no function m-add; got %v", byID)
	}
	if add.Signature.Params[0].Name != "a" || add.FanIn != 1 {
		t.Errorf("Add mapped wrong: %+v", add)
	}
	// The call's endpoints are slugs, not ints.
	if cg.Calls[0].From != "m-main" || cg.Calls[0].To != "m-add" {
		t.Errorf("call endpoints = %s->%s, want m-main->m-add", cg.Calls[0].From, cg.Calls[0].To)
	}
}

func TestEmitRefusal(t *testing.T) {
	g := contract.Graph{ContractVersion: contract.GraphVersion, Language: "python", Computable: false, NotComputableReason: "unsupported language: python"}
	cg := Emit(g)
	if cg.Computable {
		t.Error("refused graph must yield computable=false")
	}
	if cg.NotComputableReason != "unsupported language: python" {
		t.Errorf("reason = %q", cg.NotComputableReason)
	}
	if cg.Functions != nil || cg.Calls != nil {
		t.Error("refused code-graph must have nil functions/calls, not empty slices")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/code/magma && go test ./internal/architext/ -run TestEmit -v`
Expected: FAIL — `Emit`, DTOs undefined.

- [ ] **Step 3: Implement emit.go**

```go
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
```

> Note: `Emit` calls `rollup` (Task 6). To keep this task self-contained and compiling, add a temporary stub `func rollup(nodes []contract.Node, edges []contract.Edge, ids map[int]string) ([]Module, []ModuleCall) { return nil, nil }` and the `Module`/`ModuleCounts`/`ModuleCall` type declarations in a new `rollup.go` now; Task 6 replaces the stub body. Declaring the types here keeps `emit.go` compiling.

Create `internal/architext/rollup.go` with just the types + stub for now:

```go
package architext

import "github.com/robot-accomplice/magma/internal/contract"

// Module is one package in the coarse tier: its member function ids and rollup metrics.
type Module struct {
	ID          string       `json:"id"`
	Pkg         string       `json:"pkg"`
	FunctionIDs []string     `json:"function_ids"`
	Counts      ModuleCounts `json:"counts"`
	FanIn       int          `json:"fan_in"`
	FanOut      int          `json:"fan_out"`
}

// ModuleCounts summarizes a module's function population.
type ModuleCounts struct {
	Functions int `json:"functions"`
	Dead      int `json:"dead"`
	TestOnly  int `json:"test_only"`
}

// ModuleCall is one aggregated inter-module edge in the coarse tier.
type ModuleCall struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Count      int    `json:"count"`
	HasDynamic bool   `json:"has_dynamic"`
}

func rollup(nodes []contract.Node, edges []contract.Edge, ids map[int]string) ([]Module, []ModuleCall) {
	return nil, nil // replaced in Task 6
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/code/magma && go test ./internal/architext/ -run TestEmit -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/architext/emit.go internal/architext/rollup.go internal/architext/emit_test.go
git commit -m "feat(architext): Emit fine tier (functions/calls) with refusal passthrough + deterministic sort"
```

---

### Task 6: `internal/architext` — coarse tier rollup

**Files:**
- Modify: `internal/architext/rollup.go` (replace the stub body)
- Test: `internal/architext/rollup_test.go`

**Interfaces:**
- Consumes: `contract.Node.IsDead`/`IsTestOnly` (Task 1); `moduleID` (Task 4); `Module`, `ModuleCounts`, `ModuleCall` (Task 5).
- Produces: real `rollup(nodes, edges, ids)` — packages → modules with counts + fan; inter-module edges aggregated with count + has_dynamic.

- [ ] **Step 1: Write the failing test**

`internal/architext/rollup_test.go`:

```go
package architext

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestRollupAggregates(t *testing.T) {
	nodes := []contract.Node{
		{ID: 0, Pkg: "a", Symbol: "Main", Root: true, Reachable: true, ProdReachable: true},
		{ID: 1, Pkg: "a", Symbol: "Dead"}, // unreachable, non-root -> dead
		{ID: 2, Pkg: "b", Symbol: "Helper", Reachable: true, ProdReachable: true},
	}
	edges := []contract.Edge{
		{From: 0, To: 2, Kind: "dynamic"}, // a -> b, dynamic
		{From: 2, To: 2, Kind: "static"},  // b -> b, intra-module (excluded from module_calls)
	}
	ids := assignIDs(nodes)
	mods, mcalls := rollup(nodes, edges, ids)

	byID := map[string]Module{}
	for _, m := range mods {
		byID[m.ID] = m
	}
	if byID["a"].Counts.Functions != 2 || byID["a"].Counts.Dead != 1 {
		t.Errorf("module a counts = %+v, want functions=2 dead=1", byID["a"].Counts)
	}
	// Module fan = distinct module-graph degree: a has one out-edge (a->b), no in;
	// b has one in-edge (from a), no out (its self-edge b->b is intra-module).
	if byID["a"].FanOut != 1 || byID["a"].FanIn != 0 {
		t.Errorf("module a fan = in%d out%d, want in0 out1", byID["a"].FanIn, byID["a"].FanOut)
	}
	if byID["b"].FanIn != 1 || byID["b"].FanOut != 0 {
		t.Errorf("module b fan = in%d out%d, want in1 out0", byID["b"].FanIn, byID["b"].FanOut)
	}
	if len(mcalls) != 1 {
		t.Fatalf("module_calls = %d, want 1 (a->b; intra-module b->b excluded)", len(mcalls))
	}
	if mcalls[0].From != "a" || mcalls[0].To != "b" || !mcalls[0].HasDynamic || mcalls[0].Count != 1 {
		t.Errorf("module_call = %+v, want a->b count=1 has_dynamic=true", mcalls[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/code/magma && go test ./internal/architext/ -run TestRollup -v`
Expected: FAIL — stub returns nil.

- [ ] **Step 3: Implement rollup**

Replace the stub `rollup` in `internal/architext/rollup.go`:

```go
import "sort" // add to the import block alongside the contract import

func rollup(nodes []contract.Node, edges []contract.Edge, ids map[int]string) ([]Module, []ModuleCall) {
	pkgOf := make(map[int]string, len(nodes)) // node.ID -> package
	mods := map[string]*Module{}
	for _, n := range nodes {
		pkgOf[n.ID] = n.Pkg
		mid := moduleID(n.Pkg)
		m, ok := mods[mid]
		if !ok {
			m = &Module{ID: mid, Pkg: n.Pkg}
			mods[mid] = m
		}
		m.FunctionIDs = append(m.FunctionIDs, ids[n.ID])
		m.Counts.Functions++
		if n.IsDead() {
			m.Counts.Dead++
		}
		if n.IsTestOnly() {
			m.Counts.TestOnly++
		}
	}

	type mkey struct{ from, to string }
	agg := map[mkey]*ModuleCall{}
	for _, e := range edges {
		from, to := moduleID(pkgOf[e.From]), moduleID(pkgOf[e.To])
		if from == to {
			continue // intra-module calls are not inter-module edges
		}
		k := mkey{from, to}
		mc, ok := agg[k]
		if !ok {
			mc = &ModuleCall{From: from, To: to}
			agg[k] = mc
		}
		mc.Count++
		if e.Kind == "dynamic" {
			mc.HasDynamic = true
		}
	}

	// Module fan is the module-graph degree: the number of DISTINCT inter-module
	// edges in/out of each module, NOT the summed underlying call counts. Counting
	// one per aggregated module_call (after dedup) is what makes it "distinct" — a
	// module pair joined by many underlying calls still contributes exactly 1.
	for _, mc := range agg {
		mods[mc.From].FanOut++
		mods[mc.To].FanIn++
	}

	outMods := make([]Module, 0, len(mods))
	for _, m := range mods {
		sort.Strings(m.FunctionIDs)
		outMods = append(outMods, *m)
	}
	outCalls := make([]ModuleCall, 0, len(agg))
	for _, mc := range agg {
		outCalls = append(outCalls, *mc)
	}
	return outMods, outCalls
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/code/magma && go test ./internal/architext/ -v`
Expected: PASS (rollup + emit tests; `Emit` now returns populated coarse tier).

- [ ] **Step 5: Commit**

```bash
git add internal/architext/rollup.go internal/architext/rollup_test.go
git commit -m "feat(architext): coarse-tier module rollup (counts, fan, inter-module edges)"
```

---

### Task 7: `internal/architext` — `Write` (dual destination, deterministic bytes)

**Files:**
- Create: `internal/architext/write.go`
- Test: `internal/architext/write_test.go`

**Interfaces:**
- Consumes: `CodeGraph` (Task 5).
- Produces: `func Write(cg CodeGraph, dests ...string) error` — marshals once, writes the same bytes to every dest path, creating parent dirs. `const FileName = "code-graph.json"`.

- [ ] **Step 1: Write the failing test**

`internal/architext/write_test.go`:

```go
package architext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteIsDeterministicAndDual(t *testing.T) {
	cg := Emit(sampleGraph())
	d1 := filepath.Join(t.TempDir(), "repo", "docs", "architext", "data", FileName)
	d2 := filepath.Join(t.TempDir(), "vault", ".magma", FileName)
	if err := Write(cg, d1, d2); err != nil {
		t.Fatal(err)
	}
	b1, err := os.ReadFile(d1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(d2)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Error("dual-written files differ")
	}
	if b1[len(b1)-1] != '\n' {
		t.Error("output must end in a newline")
	}
	// Determinism: emitting + marshalling the same graph again is byte-identical.
	d3 := filepath.Join(t.TempDir(), FileName)
	if err := Write(Emit(sampleGraph()), d3); err != nil {
		t.Fatal(err)
	}
	b3, _ := os.ReadFile(d3)
	if string(b3) != string(b1) {
		t.Error("same graph produced different bytes across runs")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/code/magma && go test ./internal/architext/ -run TestWrite -v`
Expected: FAIL — `Write`/`FileName` undefined.

- [ ] **Step 3: Implement write.go**

```go
package architext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the fixed artifact name written into each destination directory.
const FileName = "code-graph.json"

// Write marshals cg once (indented, newline-terminated) and writes the identical
// bytes to every destination path, creating parent directories as needed. Each
// dest is a full file path (…/code-graph.json). Marshalling once guarantees the
// dual-written copies are byte-identical.
func Write(cg CodeGraph, dests ...string) error {
	b, err := json.MarshalIndent(cg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling code-graph: %w", err)
	}
	b = append(b, '\n')
	for _, dest := range dests {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, b, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/code/magma && go test ./internal/architext/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/architext/write.go internal/architext/write_test.go
git commit -m "feat(architext): Write — deterministic, dual-destination artifact writer"
```

---

### Task 8: Wire `--architext` into the CLI (flag, dual-write, freshness)

**Files:**
- Modify: `main.go` (`options` struct + `parseArgs` ~line 109-145; `run` ~line 181-251; `isFresh` ~line 265-305; usage text ~line 146-179)
- Test: `main_test.go`

**Interfaces:**
- Consumes: `architext.Emit`, `architext.Write`, `architext.FileName` (Tasks 5/7).
- Produces: `--architext[=DIR]` flag; when set, `run` writes `code-graph.json` to `<repo>/docs/architext/data/` and the vault `<out>/.magma/`; `isFresh` accounts for the artifact.

- [ ] **Step 1: Write the failing test**

Add to `main_test.go` (match the existing test package/helpers — the repo's `main_test.go` already builds fixtures and calls `run`/`parseArgs`; follow its established pattern for constructing a temp repo). Minimal flag + emission test:

```go
func TestParseArgsArchitextFlag(t *testing.T) {
	o := parseArgs([]string{"--architext", "repo", "name", "out"})
	if !o.architext {
		t.Error("--architext should set architext=true")
	}
	if o.architextDir != "" {
		t.Errorf("bare --architext should leave architextDir empty (default), got %q", o.architextDir)
	}
	o2 := parseArgs([]string{"--architext=/tmp/x", "repo", "name", "out"})
	if o2.architextDir != "/tmp/x" {
		t.Errorf("architextDir = %q, want /tmp/x", o2.architextDir)
	}
}
```

Add an emission test that runs magma with `--architext` against the `internal/backend/testdata/sigmod` fixture repo and asserts both files exist and validate as JSON. Use the same repo-construction/`run` harness the existing `main_test.go` tests use; assert `<repo>/docs/architext/data/code-graph.json` and `<out>/<name>/.magma/code-graph.json` both exist and contain `"contract_version": "magma-code-graph/1"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/code/magma && go test . -run TestParseArgsArchitextFlag -v`
Expected: FAIL — `o.architext`/`o.architextDir` fields undefined.

- [ ] **Step 3: Add flag fields + parsing**

In the `options` struct add:

```go
	architext    bool
	architextDir string // "" => default: <repo>/docs/architext/data
```

In `parseArgs`, in the flag switch, add cases (mirror the existing `--force`/`-f` and value-flag handling):

```go
		case a == "--architext":
			opts.architext = true
		case strings.HasPrefix(a, "--architext="):
			opts.architext = true
			opts.architextDir = strings.TrimPrefix(a, "--architext=")
```

- [ ] **Step 4: Wire emission into `run` + freshness**

In `run`, after the graph is built and the `.magma/` artifacts are written (after `emitReport`/`writeArtifacts`), add the architext emission. `repoArg` is the analyzed repo; `dataDir` is `<out>/.magma`:

```go
	if opts.architext {
		repoAbs, err := filepath.Abs(repoArg)
		if err != nil {
			return fmt.Errorf("resolving repo path: %w", err)
		}
		archDir := opts.architextDir
		if archDir == "" {
			archDir = filepath.Join(repoAbs, "docs", "architext", "data")
		}
		cg := architext.Emit(g)
		if err := architext.Write(cg,
			filepath.Join(archDir, architext.FileName),
			filepath.Join(dataDir, architext.FileName),
		); err != nil {
			return fmt.Errorf("writing architext code-graph: %w", err)
		}
	}
```

Add `"github.com/robot-accomplice/magma/internal/architext"` to the imports.

In `isFresh`, change the signature to accept whether architext output is required and check the vault copy:

```go
func isFresh(out string, meta contract.Meta, wantArchitext bool) bool {
```

At the point where it verifies the other `.magma/*.json` files exist, add:

```go
	if wantArchitext {
		if _, err := os.Stat(filepath.Join(dataDir, architext.FileName)); err != nil {
			return false // architext requested but its artifact is missing -> rebuild
		}
	}
```

Update the caller in `run` (~line 228): `if !opts.force && isFresh(out, meta, opts.architext) {`.

- [ ] **Step 5: Add a line to the usage text**

In `usageText()`, add under the flags block (keep the existing alignment):

```
"      --architext[=DIR]  also emit docs/architext/data/code-graph.json (default: <repo>/docs/architext/data)\n" +
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd ~/code/magma && go test . -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add main.go main_test.go
git commit -m "feat(cli): --architext[=DIR] emits code-graph.json to repo + vault, freshness-aware"
```

---

### Task 9: Full-suite verification + clean-code / clean-architecture self-review

**Files:** none created; verification + fixes only.

- [ ] **Step 1: Format, vet, test, coverage**

```bash
cd ~/code/magma
gofmt -l .            # expect: no output
go vet ./...          # expect: no output
go test ./... -coverprofile=cover.out
go tool cover -func=cover.out | tail -1   # expect: total >= 80.0%
```
Expected: all clean; total coverage ≥ 80%. If `internal/architext` drags total below the bar, add table cases to the emit/rollup tests (they are pure functions — cheap to cover).

- [ ] **Step 2: Real end-to-end run (magma on itself)**

```bash
cd ~/code/magma && go build -o /tmp/magma .
/tmp/magma --architext . magma /tmp/mgvault
test -f docs/architext/data/code-graph.json && echo "repo-side OK"
test -f /tmp/mgvault/magma/.magma/code-graph.json && echo "vault-side OK"
diff docs/architext/data/code-graph.json /tmp/mgvault/magma/.magma/code-graph.json && echo "dual copies identical"
```
Expected: both files present, byte-identical. Inspect one function node to confirm signature/doc/fan are populated:
```bash
jq '.functions[] | select(.symbol=="BuildGraph") | {id,signature,fan_in,fan_out,doc}' docs/architext/data/code-graph.json
```

- [ ] **Step 3: Determinism check**

```bash
/tmp/magma --force --architext . magma /tmp/mgvault
git diff --stat docs/architext/data/code-graph.json   # expect: no change (same SHA -> same bytes)
```
Expected: no diff (byte-deterministic). Note: `docs/architext/data/` will be committed for the dogfood in Spec B; for now confirm the no-diff property, then `git checkout` the file if you do not intend to commit it yet.

- [ ] **Step 4: Boundary check (clean-architecture dependency rule)**

```bash
grep -rn "internal/architext" internal/contract internal/backend || echo "OK: core does not import the adapter"
```
Expected: `OK` — the dependency arrow points inward only.

- [ ] **Step 5: Re-invoke the quality skills on the diff**

Invoke `/clean-code` and `/clean-architecture` on the full branch diff; fix any findings inline (small units, intent-revealing names, no magic literals, tests-encode-why). Re-run Step 1 after any change.

- [ ] **Step 6: Commit any fixes**

```bash
git add -A && git commit -m "chore: coverage top-up + clean-code/clean-architecture self-review fixes"
```

---

## Self-Review

**Spec coverage** (against `2026-07-24-magma-architext-emitter-design.md`):
- Node enrichment (params+types, results, doc, fan) → Tasks 1–3. ✓
- `magma-code-graph/1` contract (envelope, fine `functions`/`calls`, coarse `modules`/`module_calls`) → Tasks 5–6; version const in Task 5. ✓
- Deterministic slug ids + collision resolution → Task 4. ✓
- Output adapter as inward-only boundary → Tasks 4–7; verified Task 9 Step 4. ✓
- Dual write (repo `docs/architext/data/` + vault `.magma/`) → Tasks 7–8. ✓
- `--architext[=DIR]` flag + freshness → Task 8. ✓
- Honest refusal passthrough → Task 5 (`TestEmitRefusal`). ✓
- Determinism / no timestamps → Task 5 `sortCodeGraph`, Task 7 single-marshal, Task 9 Step 3. ✓
- 80% coverage, gofmt/vet clean → Task 9. ✓
- Clean-architecture / clean-code sections → boundary test (Task 9 Step 4) + skill re-invocation (Task 9 Step 5). ✓

**Placeholder scan:** the only forward reference is `rollup` (Task 5 declares types + a stub so `emit.go` compiles; Task 6 fills the body) — called out explicitly in Task 5 Step 3. No `TBD`/"handle edge cases"/bare "write tests" remain.

**Type consistency:** `Signature`/`Param`/`Result` (contract, Task 1) are reused by the backend (Task 2) and embedded in `Function` (Task 5). `assignIDs` (Task 4) returns `map[int]string`, consumed by `Emit` and `rollup` with that exact type. `Emit`/`Write`/`FileName`/`Module`/`ModuleCall`/`ModuleCounts` names match across Tasks 5–8. `isFresh` gains a third `bool` param, and its sole caller in `run` is updated in the same task (Task 8 Step 4).
```
