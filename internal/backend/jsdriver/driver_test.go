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
		From *int `json:"from"`
		To   *int `json:"to"`
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

// EVERY EMITTED EDGE MUST NAME A DECLARED NODE — the invariant belongs to the
// producer, because no downstream layer can repair a violation.
//
// A call at module scope (`main()` at the foot of an index.js) is not inside
// any function. It used to be attributed to the sentinel -1, which left the
// graph: the architext emit reads `ids[e.From]` from a map[int]string, whose
// zero value is "", so it arrived at the consumer as `"from": ""` and failed
// their `^[a-z][a-z0-9-]*$` id pattern — rejecting the ENTIRE artifact over one
// row. Where the endpoints' module slugs differed it was worse still: the
// rollup dereferenced a nil module and panicked the whole run.
//
// Module scope now has a real node, so this asserts the property rather than
// the absence of one magic number.
func TestEveryCallEndpointNamesADeclaredFunction(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A top-level call, in a subdirectory: the exact shape that panicked.
	if err := os.WriteFile(filepath.Join(repo, "src", "index.js"),
		[]byte("function helper() {}\nfunction main() { helper(); }\nmain();\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := run(t, repo)

	declared := map[int]bool{}
	for _, f := range env.Functions {
		declared[f.ID] = true
	}
	if len(env.Calls) == 0 {
		t.Fatal("no calls emitted; main() calls helper() and the module calls main()")
	}
	for i, c := range env.Calls {
		if c.From == nil || c.To == nil {
			t.Errorf("call %d has a missing endpoint (from=%v to=%v)", i, c.From, c.To)
			continue
		}
		if !declared[*c.From] {
			t.Errorf("call %d: from=%d is not a declared function", i, *c.From)
		}
		if !declared[*c.To] {
			t.Errorf("call %d: to=%d is not a declared function", i, *c.To)
		}
	}
}

// Module scope is executable code in JavaScript, so it gets a real node rather
// than being dropped — dropping the edge instead would make `main`, called only
// from module scope, report DEAD. A dead row is a deletion order, so a false
// one is the cardinal sin; an extra honest node is not.
func TestModuleScopeIsANode(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "index.js"),
		[]byte("function main() {}\nmain();\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := run(t, repo)

	var mod *int
	for i, f := range env.Functions {
		if f.Symbol == "<module>" && f.File == "index.js" {
			id := env.Functions[i].ID
			mod = &id
		}
	}
	if mod == nil {
		t.Fatalf("no module-scope node for index.js; got %+v", env.Functions)
	}
	found := false
	for _, c := range env.Calls {
		if c.From != nil && *c.From == *mod {
			found = true
		}
	}
	if !found {
		t.Error("the top-level main() call was not attributed to the module node")
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
