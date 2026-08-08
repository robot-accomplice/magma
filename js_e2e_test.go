package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// End to end on a real JS repo through the whole pipeline, not just the
// backend: run() must emit a computable graph carrying the backend's
// limitations and this run's disclosure.
func TestRunJavaScriptRepo(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	repo := gitRepo(t, map[string]string{
		"package.json": `{"name":"p","version":"0.0.0","main":"index.js"}`,
		"index.ts":     "export function live() { helper(); }\nfunction helper() {}\nfunction dead() {}\n",
	})
	outRoot := t.TempDir()
	if err := run(repo, "js", outRoot, runOpts{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	g := readGraph(t, filepath.Join(outRoot, "js", ".magma", "graph.json"))
	if !g.Computable {
		t.Fatalf("refused: %s", g.NotComputableReason)
	}
	if g.Language != "node" {
		t.Errorf("language = %q, want node", g.Language)
	}
	if g.Fidelity != "semantic" {
		t.Errorf("fidelity = %q, want semantic", g.Fidelity)
	}
	if len(g.Limitations) == 0 {
		t.Error("no limitations declared")
	}
	if g.Disclosure == nil {
		t.Error("no disclosure — every artifact must describe its run")
	}
	if len(g.Nodes) == 0 {
		t.Error("no nodes")
	}
}

// The architext emit must survive a JS repo whose sources live in a
// subdirectory, and every call it emits must resolve to a declared function.
//
// This is a regression test for a defect that reached `develop`, and the two
// halves are one bug wearing two faces. A module-scope call was attributed to
// the sentinel -1; `internal/architext/emit.go` resolves endpoints with
// `ids[e.From]` on a map[int]string, and a Go map read returns the zero value
// silently, so:
//
//   - at the repo root, both endpoints slugged to the same module, the rollup
//     skipped the edge as intra-module, and the artifact merely shipped
//     `"from": ""` — invalid against architext's id pattern, which rejects the
//     WHOLE document over one row;
//   - in a subdirectory the slugs differed, so the rollup indexed a module that
//     did not exist and PANICKED the run.
//
// A subdirectory plus a top-level call is what nearly every real Node project
// looks like, so the crashing path was the common one. `--architext` is
// required: the plain graph never exercised the rollup.
func TestArchitextEmitResolvesEveryCallEndpoint(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	repo := gitRepo(t, map[string]string{
		"package.json": `{"name":"p","version":"0.0.0","main":"src/index.js"}`,
		"src/index.js": "import { helper } from './util.js';\nfunction main() { helper(); }\nmain();\n",
		"src/util.js":  "export function helper() {}\n",
	})
	outRoot := t.TempDir()
	if err := run(repo, "js", outRoot, runOpts{architext: true}); err != nil {
		t.Fatalf("run: %v", err)
	}

	g := readGraph(t, filepath.Join(outRoot, "js", ".magma", "graph.json"))
	declared := map[int]bool{}
	for _, n := range g.Nodes {
		declared[n.ID] = true
	}
	for _, e := range g.Edges {
		if !declared[e.From] || !declared[e.To] {
			t.Errorf("edge %d->%d at %s:%d references a node the graph does not declare",
				e.From, e.To, e.File, e.Line)
		}
	}
}
