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
