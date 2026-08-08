package jsdriver

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/robot-accomplice/magma/internal/backend/jsvendor"
)

type envelope struct {
	ContractVersion     string `json:"contract_version"`
	Computable          *bool  `json:"computable"`
	Reason              string `json:"reason"`
	UnresolvedCallSites int    `json:"unresolved_call_sites"`
	Functions           []struct {
		ID     int    `json:"id"`
		Symbol string `json:"symbol"`
		File   string `json:"file"`
		Line   int    `json:"line"`
	} `json:"functions"`
	Calls []struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"calls"`
}

// run extracts the driver and compiler into a temp dir and runs it on repo.
// stdout must parse as JSON with nothing else in it — that is the contract.
func run(t *testing.T, repo string) envelope {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	if err := jsvendor.Extract(dir); err != nil {
		t.Fatal(err)
	}
	if err := Extract(dir); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", filepath.Join(dir, "driver.js"), repo)
	stdout, err := cmd.Output()
	var stderr []byte
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = ee.Stderr
	}
	var env envelope
	if e := json.Unmarshal(stdout, &env); e != nil {
		t.Fatalf("stdout is not JSON (%v)\nstdout: %s\nstderr: %s", e, stdout, stderr)
	}
	return env
}

func TestDriverEmitsFunctionsAndCalls(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "index.ts"),
		[]byte("export function live() { helper(); }\nfunction helper() {}\nfunction dead() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := run(t, repo)
	if env.ContractVersion != "magma-js-helper/1" {
		t.Fatalf("contract_version = %q", env.ContractVersion)
	}
	names := map[string]bool{}
	for _, f := range env.Functions {
		names[f.Symbol] = true
		if f.File == "" || f.Line == 0 {
			t.Errorf("function %q has no declaration site", f.Symbol)
		}
	}
	for _, want := range []string{"live", "helper", "dead"} {
		if !names[want] {
			t.Errorf("function %q missing from the envelope: %v", want, names)
		}
	}
	if len(env.Calls) == 0 {
		t.Error("no calls emitted; live() calls helper()")
	}
}

// A refusal is a real answer: exit 0 WITH an envelope, never a crash and never
// silence. Mirrors Go's and Rust's convention so magma drives all three the same.
func TestDriverRefusesEmptyRepoWithEnvelope(t *testing.T) {
	env := run(t, t.TempDir())
	if env.Computable == nil || *env.Computable {
		t.Fatalf("expected a refusal envelope, got computable=%v", env.Computable)
	}
	if env.Reason == "" {
		t.Error("a refusal must carry a reason")
	}
}

// Plain .js is first-class, not a degraded tier: TypeScript's checker is a
// JavaScript analyser with an optional type layer, so allowJs/checkJs make the
// same resolution available without annotations.
func TestPlainJavaScriptIsAnalysed(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "index.js"),
		[]byte("function helper() {}\nfunction live() { helper(); }\nmodule.exports = { live };\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := run(t, repo)
	if env.Computable != nil && !*env.Computable {
		t.Fatalf("refused a plain-JS repo: %s", env.Reason)
	}
	if len(env.Functions) < 2 {
		t.Errorf("expected both functions from plain JS, got %d", len(env.Functions))
	}
	if len(env.Calls) == 0 {
		t.Error("no calls resolved in plain JS; live() calls helper()")
	}
}

// An unresolvable call site is a fact about JavaScript; an UNCOUNTED one is a
// defect. Silence would make a limit indistinguishable from an absence.
func TestUnresolvedCallSitesAreCounted(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "dyn.js"),
		[]byte("function go(o, k) { o[k](); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := run(t, repo)
	if env.UnresolvedCallSites == 0 {
		t.Error("a computed member call must be counted as unresolved, not silently dropped")
	}
}
