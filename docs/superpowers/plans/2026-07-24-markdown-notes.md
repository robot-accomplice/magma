# Obsidian Markdown Graph Layer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit an Obsidian-navigable markdown call graph (Overview dashboard + index notes + per-function notes with `[[wikilinks]]`) alongside a hidden `.magma/` JSON contract.

**Architecture:** A new pure package `internal/notes` turns a `contract.Graph` (+ its two views) into a `map[relpath]content` of markdown files — no I/O, no clocks — so every renderer is unit-testable against fixtures. `main.go` orchestrates: build graph → write `.magma/*.json` → render notes → reconcile via a manifest → print the panel. The graph/JSON stay byte-deterministic; the sole wall-clock value ("Last validated") is passed into the renderer as a preformatted string and lands only in `Overview.md`.

**Tech Stack:** Go 1.26, stdlib only (no new deps). Obsidian renders the markdown + mermaid.

## Global Constraints

- Module path `github.com/robot-accomplice/magma`; Go 1.26; stdlib only, no new dependencies.
- `internal/notes` depends only on `internal/contract` — never on `backend`, `detect`, `gitmeta`, or `main`.
- Determinism: no `time.*` or `math/rand` inside `internal/notes` or the graph. Stable ordering everywhere (sort before emit). The one wall-clock value is `Options.Validated`, supplied by `main`, rendered only in `Overview.md`.
- Pointers not payloads: notes carry `file:line`, never source bodies.
- Coverage stays ≥ 80% overall (`go test ./... -coverprofile` gate in CI/justfile).
- Note names are plain (no leading underscore): `Overview.md`, `Dead code.md`, `Test-only code.md`, `Packages.md`, `nodes/<pkg-rel>/<Symbol>.md`.
- **Every `[[...]]` link target uses `wikiTarget(node, module)` — module-relative, matching `notePath`.** e.g. for module `ex`, node `B` in pkg `ex/sub` → file `nodes/sub/B.md`, link `[[sub/B]]` (NOT `[[ex/sub/B]]`). A node whose pkg equals the module → `[[<Symbol>]]`. Links and file paths MUST agree or Obsidian can't resolve them.
- JSON files keep their contract names (`graph.json`, `_dead.json`, `_test-only.json`) and move under `<folder>/.magma/`.
- Every commit runs `gofmt`, `go vet`, `go test ./...` green before committing.

---

### Task 1: Commit date in gitmeta

**Files:**
- Modify: `internal/contract/contract.go` (add `CommitDate` to `Meta`)
- Modify: `internal/gitmeta/gitmeta.go`
- Test: `internal/gitmeta/gitmeta_test.go`

**Interfaces:**
- Produces: `contract.Meta.CommitDate string` — ISO-8601 commit date of HEAD (e.g. `2026-07-24T14:06:00-04:00`), deterministic for a SHA. Empty on error is acceptable (Overview omits it).

- [ ] **Step 1: Write the failing test** — append to `gitmeta_test.go`:

```go
func TestLoadCommitDate(t *testing.T) {
	repo := initRepo(t) // existing helper; commits with fixed GIT_*_DATE=2020-01-01T00:00:00Z
	meta, err := Load(repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.HasPrefix(meta.CommitDate, "2020-01-01") {
		t.Errorf("CommitDate = %q, want it to start 2020-01-01", meta.CommitDate)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gitmeta/ -run TestLoadCommitDate`
Expected: FAIL — `meta.CommitDate` undefined (compile error).

- [ ] **Step 3: Implement** — add `CommitDate string` to `Meta` in `contract.go`; in `gitmeta.go` `Load`, after computing `tree`:

```go
date, _ := gitOut(repo, "show", "-s", "--format=%cI", "HEAD") // %cI = committer date, ISO-8601
return contract.Meta{SHA: sha, Tree: tree, CommitDate: date}, nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gitmeta/ -run TestLoadCommitDate`
Expected: PASS. Also `go test ./...` still green.

- [ ] **Step 5: Commit**

```bash
git add internal/contract/contract.go internal/gitmeta/gitmeta.go internal/gitmeta/gitmeta_test.go
git commit -m "feat(gitmeta): capture HEAD commit date (deterministic) in Meta"
```

---

### Task 2: Module path on the graph

**Files:**
- Modify: `internal/contract/graph.go` (add `Module` field)
- Modify: `internal/backend/golang.go` (stamp it)
- Test: `internal/backend/golang_test.go`

**Interfaces:**
- Produces: `contract.Graph.Module string` — the main module import path (e.g. `github.com/robot-accomplice/magma`). Used to make `pkg` module-relative for note paths. Additive JSON field (`"module"`), backward-compatible with the gate.

- [ ] **Step 1: Write the failing test** — append to `golang_test.go`:

```go
func TestBuildGraphStampsModule(t *testing.T) {
	g := buildFixture(t, "livemod")
	if g.Module != "magmafixture" {
		t.Errorf("Module = %q, want %q", g.Module, "magmafixture")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/backend/ -run TestBuildGraphStampsModule`
Expected: FAIL — `g.Module` undefined.

- [ ] **Step 3: Implement** — add `Module string \`json:"module"\`` to `Graph` (after `Language`); in `golang.go BuildGraph`, after `modPath := moduledPath(withTests.initial)` set `g.Module = modPath` (before assigning nodes/edges).

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/backend/ -run TestBuildGraphStampsModule`
Expected: PASS. `go test ./...` green.

- [ ] **Step 5: Commit**

```bash
git add internal/contract/graph.go internal/backend/golang.go internal/backend/golang_test.go
git commit -m "feat(contract): stamp the main module path on the graph"
```

---

### Task 3: notes package — Options + metrics

**Files:**
- Create: `internal/notes/notes.go` (package doc + `Options`)
- Create: `internal/notes/metrics.go`
- Test: `internal/notes/metrics_test.go`

**Interfaces:**
- Produces:
```go
type Options struct {
	FolderName string // <folder-name>; used in the graph-view filter recipe
	Hotspots   int    // top-N for hotspot lists/pie (default applied by caller; 0 => 10)
	Depth      int    // 0 = all reachable+unreachable (default all); N>0 = forward walk N levels
	From       string // "" = entry points; else a symbol (prettyName) to root the walk
	GraphLink  string // "" = recipe only; else Obsidian vault name for the Advanced-URI one-click link
	Validated  string // preformatted "Last validated" wall-clock string (caller supplies; keeps notes deterministic + testable)
	CommitDate string // from contract.Meta.CommitDate
}

type DegreeRow struct {
	Node   contract.Node
	Degree int
}
type Metrics struct {
	Nodes, Funcs, Methods                     int
	Edges, StaticEdges, DynamicEdges          int
	Packages                                  int
	Reachable, ProdReachable, DeadN, TestOnlyN int
	Exported, GeneratedN, TestFuncs           int
	EntryPoints                               []contract.Node // sorted by symbol
	MostCalled, MostCalling                   []DegreeRow      // top-N, sorted desc by degree then symbol
}

func computeMetrics(g contract.Graph, topN int) Metrics
```
Note: `DeadN`/`TestOnlyN` are counted from the graph itself (unreachable-non-generated-non-root; reachable-not-prodreachable-not-test-not-root-not-generated) so metrics need only the graph. `computeMetrics` is pure.

- [ ] **Step 1: Write the failing test** — `metrics_test.go`. Build a small in-memory graph (main→A→B, C dead, in/out degrees known) and assert counts + hotspot ordering:

```go
package notes

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func fixtureGraph() contract.Graph {
	g := contract.NewGraph(contract.Meta{Tree: "abc123", Fidelity: "rta"}, "go", "rta")
	g.Module = "ex"
	g.Nodes = []contract.Node{
		{ID: 0, Symbol: "main", Pkg: "ex", File: "main.go", Line: 1, Kind: "func", Root: true, Reachable: true, ProdReachable: true, Exported: false},
		{ID: 1, Symbol: "A", Pkg: "ex", File: "a.go", Line: 5, Kind: "func", Exported: true, Reachable: true, ProdReachable: true},
		{ID: 2, Symbol: "B", Pkg: "ex/sub", File: "sub/b.go", Line: 9, Kind: "func", Reachable: true, ProdReachable: true},
		{ID: 3, Symbol: "C", Pkg: "ex", File: "c.go", Line: 3, Kind: "func"}, // dead
	}
	g.Edges = []contract.Edge{
		{From: 0, To: 1, Kind: "static"}, {From: 1, To: 2, Kind: "static"}, {From: 3, To: 2, Kind: "dynamic"},
	}
	return g
}

func TestComputeMetrics(t *testing.T) {
	m := computeMetrics(fixtureGraph(), 10)
	if m.Nodes != 4 || m.Edges != 3 || m.Packages != 2 {
		t.Errorf("counts wrong: %+v", m)
	}
	if m.DeadN != 1 { // C
		t.Errorf("DeadN = %d, want 1", m.DeadN)
	}
	if len(m.EntryPoints) != 1 || m.EntryPoints[0].Symbol != "main" {
		t.Errorf("entry points wrong: %+v", m.EntryPoints)
	}
	// B has in-degree 2 (from A and C) -> most-called first
	if len(m.MostCalled) == 0 || m.MostCalled[0].Node.Symbol != "B" || m.MostCalled[0].Degree != 2 {
		t.Errorf("most-called wrong: %+v", m.MostCalled)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/notes/`
Expected: FAIL — package/functions undefined.

- [ ] **Step 3: Implement** `notes.go` (package doc + `Options`, `DegreeRow`, `Metrics` structs) and `metrics.go` (`computeMetrics`): count kinds; count edges by `Kind`; count distinct `Pkg`; count dead/test-only using the same predicates as `contract.DeadView`/`TestOnlyView` (unreachable & !generated & !root; reachable & !prodReachable & !test & !root & !generated); collect entry points (`Root && !Test`); build in/out degree maps over edges, take top-N sorted by degree desc then symbol asc.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/notes/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notes/notes.go internal/notes/metrics.go internal/notes/metrics_test.go
git commit -m "feat(notes): Options + pure graph metrics (degrees, hotspots, counts)"
```

---

### Task 4: notes package — note paths & wikilinks

**Files:**
- Create: `internal/notes/link.go`
- Test: `internal/notes/link_test.go`

**Interfaces:**
- Produces:
```go
// notePath is the folder-relative path of a node's note, e.g.
// "nodes/internal/backend/BuildGraph.md". module is the graph's Module.
func notePath(n contract.Node, module string) string
// wikiTarget is the [[...]] target (no .md), e.g. "internal/backend/BuildGraph",
// resolved by Obsidian via path suffix.
func wikiTarget(n contract.Node, module string) string
// relPkg makes an import path module-relative ("mod/internal/x" -> "internal/x"; "mod" -> ".").
func relPkg(pkg, module string) string
```

- [ ] **Step 1: Write the failing test**

```go
package notes

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestNotePathAndWikiTarget(t *testing.T) {
	n := contract.Node{Symbol: "BuildGraph", Pkg: "github.com/x/m/internal/backend"}
	if got := notePath(n, "github.com/x/m"); got != "nodes/internal/backend/BuildGraph.md" {
		t.Errorf("notePath = %q", got)
	}
	if got := wikiTarget(n, "github.com/x/m"); got != "internal/backend/BuildGraph" {
		t.Errorf("wikiTarget = %q", got)
	}
	// root package (pkg == module) collapses to nodes/<symbol>
	r := contract.Node{Symbol: "main", Pkg: "github.com/x/m"}
	if got := notePath(r, "github.com/x/m"); got != "nodes/main.md" {
		t.Errorf("root-pkg notePath = %q", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/notes/ -run TestNotePathAndWikiTarget`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** `link.go`: `relPkg` strips `module` prefix (`strings.TrimPrefix(pkg, module+"/")`; if `pkg==module` return ""); `wikiTarget` = `path.Join(rel, symbol)` (rel empty → just symbol); `notePath` = `"nodes/"+wikiTarget+".md"`. Symbols are unique within a package (Go guarantee), so paths are unique.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/notes/ -run TestNotePathAndWikiTarget`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notes/link.go internal/notes/link_test.go
git commit -m "feat(notes): deterministic note paths and path-suffix wikilinks"
```

---

### Task 5: notes package — node selection (walk)

**Files:**
- Create: `internal/notes/walk.go`
- Test: `internal/notes/walk_test.go`

**Interfaces:**
- Produces:
```go
// selectNodes returns the set of node IDs that get a per-function note.
// Depth 0 and From "" => every node. Depth N>0 => nodes within N call-levels of the
// roots (entry points, or the From symbol if set), by forward BFS over edges.
func selectNodes(g contract.Graph, depth int, from string) map[int]bool
```

- [ ] **Step 1: Write the failing test** — using `fixtureGraph()` (main→A→B, C dead):

```go
func TestSelectNodes(t *testing.T) {
	g := fixtureGraph()
	all := selectNodes(g, 0, "")
	if len(all) != 4 {
		t.Errorf("depth 0 must select all 4, got %d", len(all))
	}
	// depth 1 from entry points: main (0) + its direct callee A (1)
	d1 := selectNodes(g, 1, "")
	if !d1[0] || !d1[1] || d1[2] || d1[3] {
		t.Errorf("depth 1 wrong: %v", d1)
	}
	// from A, depth 1: A + B
	fa := selectNodes(g, 1, "A")
	if !fa[1] || !fa[2] || fa[0] || fa[3] {
		t.Errorf("from A depth 1 wrong: %v", fa)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/notes/ -run TestSelectNodes`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** `walk.go`: if `depth<=0 && from==""` return all IDs. Determine roots: if `from!=""` the node(s) whose `Symbol==from`; else nodes with `Root && !Test`. BFS forward over an adjacency map built from edges, tracking level; include a node when its level `<= depth` (treat `depth<=0` with a non-empty `from` as unlimited from that root). Return the visited set.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/notes/ -run TestSelectNodes`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notes/walk.go internal/notes/walk_test.go
git commit -m "feat(notes): depth-limited forward node selection (default all)"
```

---

### Task 6: notes package — per-function notes

**Files:**
- Create: `internal/notes/function.go`
- Test: `internal/notes/function_test.go`

**Interfaces:**
- Produces:
```go
// functionNote renders one node's markdown: YAML frontmatter + a file:line pointer +
// a "## Calls" list of [[callee]] links (deduped, sorted; dynamic edges annotated).
// callees are the node's outgoing edges resolved to nodes.
func functionNote(n contract.Node, callees []contract.Edge, byID map[int]contract.Node, module string) string
```

- [ ] **Step 1: Write the failing test**

```go
func TestFunctionNote(t *testing.T) {
	g := fixtureGraph()
	byID := map[int]contract.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	out := functionNote(byID[1], []contract.Edge{{From: 1, To: 2, Kind: "static"}}, byID, "ex")
	for _, want := range []string{
		"---", "pkg: ex", "file: a.go", "line: 5", "kind: func", "exported: true",
		"`a.go:5`", "## Calls", "[[sub/B]]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("note missing %q in:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/notes/ -run TestFunctionNote`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** `function.go`: emit frontmatter (`pkg`, `file`, `line`, `kind`, `exported`, and `tags:` including `magma/node` plus `magma/dead`/`magma/test-only`/`magma/root`/`magma/generated` per the node's booleans — dead/test-only computed with the same predicates as Task 3), then the `` `file:line` `` pointer, then `## Calls` with one `- [[wikiTarget(callee)]]` per unique callee (sorted by target; append ` (dynamic)` when the aggregated edge kind is dynamic). Skip the `## Calls` header when there are no callees.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/notes/ -run TestFunctionNote`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notes/function.go internal/notes/function_test.go
git commit -m "feat(notes): per-function note renderer (frontmatter + Calls links)"
```

---

### Task 7: notes package — index notes

**Files:**
- Create: `internal/notes/index.go`
- Test: `internal/notes/index_test.go`

**Interfaces:**
- Produces:
```go
// deadIndex / testOnlyIndex render the class notes: an explanation line + one bullet
// per row, a [[link]] when that node has a note (selected set), else `file:line` text.
func deadIndex(rows []contract.Row, g contract.Graph, selected map[int]bool) string
func testOnlyIndex(rows []contract.Row, g contract.Graph, selected map[int]bool) string
// packagesIndex lists packages -> their selected function notes as [[links]].
func packagesIndex(g contract.Graph, selected map[int]bool) string
```

- [ ] **Step 1: Write the failing test**

```go
func TestIndexNotes(t *testing.T) {
	g := fixtureGraph()
	sel := map[int]bool{0: true, 1: true, 2: true, 3: true}
	dead := deadIndex([]contract.Row{{Symbol: "C", File: "c.go", Line: 3}}, g, sel)
	if !strings.Contains(dead, "[[C]]") || !strings.Contains(dead, "candidate") {
		t.Errorf("dead index wrong:\n%s", dead)
	}
	pkgs := packagesIndex(g, sel)
	if !strings.Contains(pkgs, "[[sub/B]]") {
		t.Errorf("packages index missing B:\n%s", pkgs)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/notes/ -run TestIndexNotes`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** `index.go`. **`deadIndex` and `testOnlyIndex`** each open with a one-line explanation of the class + the "every row is a candidate, not a finding" caveat (they ARE candidate lists). For a row, find its node by `(file,line)`; if `selected`, emit `- [[wikiTarget]]`; else `- \`file:line\` symbol`. **`packagesIndex`** is a navigation index, NOT a candidate list, so it opens with a plain one-line explanation and does NOT carry the candidate caveat; it groups selected nodes by `relPkg`, sorted, each package a `##` heading with its function `[[links]]`.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/notes/ -run TestIndexNotes`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notes/index.go internal/notes/index_test.go
git commit -m "feat(notes): Dead/Test-only/Packages index notes"
```

---

### Task 8: notes package — Overview dashboard

**Files:**
- Create: `internal/notes/overview.go`
- Test: `internal/notes/overview_test.go`

**Interfaces:**
- Produces:
```go
// overview renders Overview.md: provenance callout (incl. Last validated + commit date),
// size, reachability (+ mermaid pie, + caution callout if dead>0), surface + entry-point
// links, hotspots (mermaid pie of call concentration + ranked [[link]] lists), graph-view
// recipe (+ optional Advanced-URI one-click link), a jargon-free edge-precision note, refusal
// callouts. Deterministic except opts.Validated.
func overview(g contract.Graph, dead, testOnly contract.Note, m Metrics, opts Options) string
```

- [ ] **Step 1: Write the failing test**

```go
func TestOverview(t *testing.T) {
	g := fixtureGraph()
	m := computeMetrics(g, 10)
	opts := Options{FolderName: "ex", Hotspots: 10, Validated: "2026-07-24 14:06 EDT", CommitDate: "2020-01-01T00:00:00Z"}
	out := overview(g, contract.Note{ReachabilityComputable: true}, contract.Note{ReachabilityComputable: true}, m, opts)
	for _, want := range []string{
		"magma-notes/1",           // frontmatter version marker
		"Last validated", "2026-07-24 14:06 EDT",
		"abc123",                  // tree
		"```mermaid", "pie",       // a mermaid pie is present
		"approximated",            // jargon-free edge-precision note (must NOT contain "fidelity" or "RTA")
		"⌘/Ctrl", `path:"ex/nodes"`, // graph-view recipe
		"[[sub/B]]",               // a hotspot link (module-relative wikiTarget)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q in:\n%s", want, out)
		}
	}
}

func TestOverviewRefusalCallout(t *testing.T) {
	g := contract.NewGraph(contract.Meta{Tree: "abc"}, "rust", "").Refuse(`language "rust" not built yet`)
	out := overview(g, contract.Note{}, contract.Note{}, Metrics{}, Options{FolderName: "p"})
	if !strings.Contains(out, "[!failure]") || !strings.Contains(out, "rust") {
		t.Errorf("refused graph must show a failure callout:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/notes/ -run TestOverview`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** `overview.go` per the spec's dashboard section: frontmatter with `magma-notes/1`; `> [!info]` provenance (repo/module, language, tree with `> [!warning]` when `-dirty`, commit date, magma version via `g.Generator`, Last validated); size table; reachability (counts + a `pie` of ProdReachable/TestOnly/Dead, `> [!caution]` if dead>0); surface (exported count, entry-point `[[links]]`); hotspots (`pie` of top-N in-degree + `(others)`, then ranked `[[link]]` lists); graph-view recipe using `opts.FolderName` (`path:"<folder>/nodes"`) and, when `opts.GraphLink != ""`, an `obsidian://advanced-uri?vault=<GraphLink>&commandid=graph%3Aopen` link; a **jargon-free edge-precision note** — no "fidelity" or "RTA" wording — e.g. "Direct calls are exact; calls through interfaces or function values are approximated (may include paths that can't occur at runtime, but never miss a real one)."; an entry-point→package **mermaid `flowchart`** (each entry point node linking to the distinct packages it transitively reaches, kept to top-level so it stays legible — dedupe edges, sort, cap at a sane number of packages and note if truncated); `> [!failure]` callouts when `!g.Computable` or a view refused. Refused graph short-circuits to provenance + failure callout. (The `TestOverview` assertion covers the pie + recipe + fidelity; add an assertion for `flowchart` presence.)

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/notes/ -run TestOverview`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notes/overview.go internal/notes/overview_test.go
git commit -m "feat(notes): Overview repository-state dashboard"
```

---

### Task 9: notes package — Render orchestration + reconcile

**Files:**
- Modify: `internal/notes/notes.go` (add `Render`, `Reconcile`)
- Test: `internal/notes/notes_test.go`

**Interfaces:**
- Produces:
```go
// Render is the package entrypoint: it produces every markdown file for the map,
// keyed by folder-relative path, plus the sorted manifest of those paths. Pure.
func Render(g contract.Graph, dead, testOnly contract.Note, opts Options) (files map[string]string, manifest []string)
// Reconcile returns the folder-relative paths present in oldManifest but not in
// newManifest — stale notes a caller should delete.
func Reconcile(oldManifest, newManifest []string) []string
```

- [ ] **Step 1: Write the failing test**

```go
func TestRenderProducesExpectedFiles(t *testing.T) {
	g := fixtureGraph() // module "ex"; pkg "ex/sub" -> rel "sub" -> nodes/sub/B.md
	files, manifest := Render(g,
		contract.Note{ReachabilityComputable: true, Rows: []contract.Row{{Symbol: "C", File: "c.go", Line: 3}}},
		contract.Note{ReachabilityComputable: true},
		Options{FolderName: "ex", Hotspots: 10, Validated: "t"})
	for _, want := range []string{"Overview.md", "Dead code.md", "Test-only code.md", "Packages.md", "nodes/sub/B.md", "nodes/main.md"} {
		if _, ok := files[want]; !ok {
			t.Errorf("Render missing file %q; got keys %v", want, keysOf(files))
		}
	}
	if len(manifest) != len(files) {
		t.Errorf("manifest (%d) must list every file (%d)", len(manifest), len(files))
	}
	// manifest is sorted
	for i := 1; i < len(manifest); i++ {
		if manifest[i-1] > manifest[i] {
			t.Errorf("manifest not sorted at %d: %q > %q", i, manifest[i-1], manifest[i])
		}
	}
}

func keysOf(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestReconcile(t *testing.T) {
	stale := Reconcile([]string{"a.md", "nodes/x/Old.md", "Overview.md"}, []string{"a.md", "Overview.md"})
	if len(stale) != 1 || stale[0] != "nodes/x/Old.md" {
		t.Errorf("Reconcile = %v, want [nodes/x/Old.md]", stale)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/notes/ -run 'TestRender|TestReconcile'`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** `Render`: apply `Hotspots` default (0→10); `selected := selectNodes(g, opts.Depth, opts.From)`; compute metrics; assemble `files`: `Overview.md`, `Dead code.md`, `Test-only code.md`, `Packages.md`, and for each selected node a `notePath → functionNote(...)`; `manifest` = sorted keys. When `!g.Computable`, emit only `Overview.md` (refusal). `Reconcile`: set-difference old−new, sorted.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/notes/`
Expected: PASS. Check `go test ./internal/notes/ -cover` ≥ 80%.

- [ ] **Step 5: Commit**

```bash
git add internal/notes/notes.go internal/notes/notes_test.go
git commit -m "feat(notes): Render orchestration + manifest reconcile"
```

---

### Task 10: main — wire notes, .magma/ relocation, flags, freshness, panel

**Files:**
- Modify: `main.go`
- Modify: `main_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `notes.Render`, `notes.Reconcile`, `notes.Options`, `contract.Meta.CommitDate`, `contract.Graph.Module`.
- Produces: on disk under `<vault>/<folder>/`: the markdown notes, `.magma/{graph,_dead,_test-only}.json`, `.magma/manifest.json`. New flags `--depth`, `--from`, `--hotspots`, `--graph-link`. Panel gains a `notes` count; the `fidelity` row is removed (constant `rta`, noise to humans; the JSON field stays for machines).

- [ ] **Step 1: Write the failing test** — extend `main_test.go`:

```go
func TestRunWritesVaultAndHiddenJSON(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"go.mod":  "module tmpmod\n\ngo 1.21\n",
		"main.go": "package main\nfunc main(){ Live() }\nfunc Live(){}\nfunc Dead(){}\n",
	})
	outRoot := t.TempDir()
	if err := run(repo, "proj", outRoot, runOpts{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	base := filepath.Join(outRoot, "proj")
	for _, p := range []string{"Overview.md", "Dead code.md", ".magma/graph.json", ".magma/manifest.json"} {
		if _, err := os.Stat(filepath.Join(base, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	// JSON no longer at folder root
	if _, err := os.Stat(filepath.Join(base, "graph.json")); err == nil {
		t.Error("graph.json must live under .magma/, not the folder root")
	}
}

func TestRunReconcilesStaleNotes(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"go.mod":  "module tmpmod\n\ngo 1.21\n",
		"main.go": "package main\nfunc main(){ Live() }\nfunc Live(){}\n",
	})
	outRoot := t.TempDir()
	base := filepath.Join(outRoot, "proj")
	if err := run(repo, "proj", outRoot, runOpts{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Plant a note and add it to the manifest so magma believes it wrote it last time.
	stale := filepath.Join(base, "nodes", "gone", "Removed.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf := filepath.Join(base, ".magma", "manifest.json")
	var manifest []string
	readJSON(t, mf, &manifest)
	manifest = append(manifest, "nodes/gone/Removed.md")
	writeJSONFile(t, mf, manifest)
	// A human note magma never wrote must survive.
	foreign := filepath.Join(base, "My notes.md")
	if err := os.WriteFile(foreign, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run(repo, "proj", outRoot, runOpts{force: true}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale note listed in the manifest must be deleted on regen")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("a foreign note magma never wrote must be left alone")
	}
}
```

Note: `run`'s signature changes to `run(repoArg, name, outRoot string, opts runOpts)` where `runOpts{force bool; depth int; from, graphLink string; hotspots int}`. Update all existing `run(...)` call sites in `main_test.go` accordingly (`run(repo, "proj", outRoot, runOpts{})`).

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run TestRunWritesVault`
Expected: FAIL — `.magma/` files absent / `runOpts` undefined.

- [ ] **Step 3: Implement** in `main.go`:
  1. Add `runOpts` struct; thread through `parseArgs` (new flags `--depth N`, `--from S`, `--hotspots N`, `--graph-link[=vault]`); update `usageText` + README with the new flags and note names.
  2. `prepareOutput` unchanged; add `dataDir := filepath.Join(out, ".magma")` (MkdirAll).
  3. `isFresh` now reads `<out>/.magma/graph.json` (clean tree + this generator, as before) AND requires the full markdown asset set to be present — a JSON-only folder from an older run must NOT count as fresh. Concretely: `.magma/manifest.json` must exist, and **every markdown path it lists must exist on disk** (a deleted/partial `.md` set forces a rebuild). If the manifest is missing or any listed file is absent, `isFresh` returns false. (Add a test `TestRunNotFreshWhenMarkdownMissing`: run once, delete `Overview.md`, run again without `--force`, assert the map was rebuilt — the file reappears.)
  4. On fresh-skip: read `.magma/graph.json`, re-render **only** `Overview.md` (via `notes.Render` on the parsed graph, keep just that file) with a fresh `Validated`, write it, print the "already fresh" panel.
  5. Build path: write `.magma/{graph,_dead,_test-only}.json` (existing writers, new dir); call `notes.Render(g, dead, testOnly, opts)`; write each file (creating parent dirs); read prior `.magma/manifest.json` (if any), `notes.Reconcile`, delete stale files; write new `manifest.json`. Guard every write path stays within `out` (reject symlink/escape).
  6. `Options.Validated` = `time.Now().Format("2006-01-02 15:04 MST")` (the only clock; lives in `main`). `Options.CommitDate` = `meta.CommitDate`. `Options.GraphLink` from the flag.
  7. Panel: add a `notes` row (len(files)); **REMOVE the `fidelity` row entirely** — it is a constant `rta` today and is noise to a human. (The `fidelity` field stays in the JSON contract for the gate + future backends; it is just not shown in the terminal panel.)

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./...` ; `just ci`
Expected: PASS; coverage ≥ 80%.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go README.md
git commit -m "feat: write Obsidian markdown notes + hidden .magma/ JSON, new flags, reconcile"
```

---

### Task 11: End-to-end verification on a real vault

**Files:** none (verification + docs polish only)

- [ ] **Step 1** Build and run against a throwaway clean repo into a temp vault: `go build -o /tmp/magma . && /tmp/magma <clean-repo> demo /tmp/vault`; open `/tmp/vault/demo/Overview.md` and confirm the dashboard renders (mermaid pie, callouts, links) and `nodes/` notes cross-link. Run against roboticus (`--depth 2` to keep it light) and confirm scale behavior + `.magma/` JSON still matches the deadcode oracle (unchanged analysis).
- [ ] **Step 2** Confirm determinism: run twice on a clean repo; `diff -r` the two outputs excluding `Overview.md` → identical; `Overview.md` differs only in the "Last validated" line.
- [ ] **Step 3** `just ci` green; update `HANDOFF.md` state; commit any doc fixes.

```bash
git add HANDOFF.md
git commit -m "docs: record markdown-notes delivery state"
```
