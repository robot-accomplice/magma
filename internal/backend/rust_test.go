package backend

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
)

// The Rust backend registers itself for detect.Rust on init.
func TestRustBackendRegistered(t *testing.T) {
	b, ok := For(detect.Rust)
	if !ok {
		t.Fatal("no backend registered for Rust")
	}
	if b.Language() != "rust" {
		t.Errorf("Language() = %q, want %q", b.Language(), "rust")
	}
}

// A missing helper is a REFUSAL carrying an actionable reason, never an error
// and never a silent empty graph — the same contract a Go module with no
// entrypoint gets. The reason text is user-facing, so it must name both the
// binary and a way to supply it; a bare "not found" would leave the user stuck.
func TestRustMissingHelperRefuses(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(helperEnv, "")

	meta := contract.Meta{Generator: "magma/test", SHA: "deadbee", Tree: "deadbee"}
	b, _ := For(detect.Rust)
	g, err := b.BuildGraph(t.TempDir(), meta, nil)
	if err != nil {
		t.Fatalf("a missing helper must refuse, not error: %v", err)
	}
	if g.Computable {
		t.Fatal("graph must not be computable without the helper")
	}
	for _, want := range []string{helperBin, helperEnv, "cargo install"} {
		if !strings.Contains(g.NotComputableReason, want) {
			t.Errorf("refusal reason must mention %q, got: %s", want, g.NotComputableReason)
		}
	}
	if g.Nodes != nil || g.Edges != nil {
		t.Error("a refused graph must carry no nodes or edges")
	}
}

// An explicit MAGMA_RUST_HELPER pointing at nothing is also a refusal, and it
// must name the path so the user can see what was tried.
func TestRustBadHelperEnvRefuses(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	t.Setenv(helperEnv, missing)

	b, _ := For(detect.Rust)
	g, err := b.BuildGraph(t.TempDir(), contract.Meta{}, nil)
	if err != nil {
		t.Fatalf("an unusable helper path must refuse, not error: %v", err)
	}
	if g.Computable {
		t.Fatal("graph must not be computable")
	}
	if !strings.Contains(g.NotComputableReason, missing) {
		t.Errorf("refusal must name the bad path, got: %s", g.NotComputableReason)
	}
}

// THE REGRESSION THIS FILE EXISTS FOR.
//
// The all-roots seed must use `test_entry` (a literal #[test] attribute), NOT
// the broader `test` (which also covers #[cfg(test)] ancestry and
// tests/benches targets). Seeding on `test` makes every function living under
// a test module its own root, so it trivially reaches itself and a genuinely
// dead test helper can never be reported — the exact false-agreement failure
// the oracle harness exists to catch.
//
// The fixture below is built so the two choices give DIFFERENT answers, which
// is what makes this a real test rather than a restatement of the code:
//
//	id 0  prod          root, production entry
//	id 1  helper        called by prod
//	id 2  real_test     test_entry + test — a genuine #[test]
//	id 3  test_helper   test only, called BY real_test  -> live under tests
//	id 4  dead_helper   test only, called by NOBODY     -> must stay dead
//
// dead_helper carries `test: true` and `test_entry: false`. Seeding on `test`
// would make it its own root and report it reachable; only `test_entry` keeps
// it correctly unreachable.
func TestRustReachabilitySeedsOnTestEntryNotTest(t *testing.T) {
	fns := []helperFunction{
		{ID: 0, Symbol: "prod", Root: true},
		{ID: 1, Symbol: "helper"},
		{ID: 2, Symbol: "real_test", Test: true, TestEntry: true},
		{ID: 3, Symbol: "test_helper", Test: true},
		{ID: 4, Symbol: "dead_helper", Test: true},
	}
	nodes := make([]contract.Node, 0, len(fns))
	for _, f := range fns {
		nodes = append(nodes, contract.Node{ID: int(f.ID), Symbol: f.Symbol, Test: f.Test, Root: f.Root})
	}
	edges := []contract.Edge{{From: 0, To: 1}, {From: 2, To: 3}}

	markReachable(nodes, edges, fns)

	want := map[string]struct{ reach, prod bool }{
		"prod":        {true, true},
		"helper":      {true, true},
		"real_test":   {true, false},
		"test_helper": {true, false},
		"dead_helper": {false, false}, // the assertion that fails if `test` is used as the seed
	}
	for _, n := range nodes {
		w := want[n.Symbol]
		if n.Reachable != w.reach || n.ProdReachable != w.prod {
			t.Errorf("%s: Reachable=%v ProdReachable=%v, want %v/%v",
				n.Symbol, n.Reachable, n.ProdReachable, w.reach, w.prod)
		}
	}
}

// A #[bench] entry point is a harness entry like #[test]: reachable, but never
// production-reachable. Without this, benchmark-only code reads as dead.
func TestRustBenchIsHarnessRootNotProdRoot(t *testing.T) {
	fns := []helperFunction{
		{ID: 0, Symbol: "bench_it", Bench: true, Test: true},
		{ID: 1, Symbol: "benched"},
	}
	nodes := []contract.Node{
		{ID: 0, Symbol: "bench_it", Test: true},
		{ID: 1, Symbol: "benched"},
	}
	markReachable(nodes, []contract.Edge{{From: 0, To: 1}}, fns)

	if !nodes[1].Reachable {
		t.Error("code reached only from a #[bench] entry must be Reachable")
	}
	if nodes[1].ProdReachable {
		t.Error("a #[bench] entry is not a production root")
	}
}

// A refusal envelope from the helper becomes a refused graph carrying the
// helper's own reason. Told apart from an Output by the refusal-only
// `computable` field, so a real workspace with zero functions is NOT mistaken
// for a refusal — the second half of this test.
func TestRustEnvelopeRefusalVsEmptyGraph(t *testing.T) {
	var refusal helperEnvelope
	mustUnmarshal(t, `{"contract_version":"magma-rust-helper/1","executed_target_code":true,
		"computable":false,"reason":"no roots in scope"}`, &refusal)
	if refusal.Computable == nil || *refusal.Computable {
		t.Fatal("a refusal envelope must decode computable:false")
	}

	var empty helperEnvelope
	mustUnmarshal(t, `{"contract_version":"magma-rust-helper/1","executed_target_code":true,
		"functions":[],"calls":[]}`, &empty)
	if empty.Computable != nil {
		t.Error("an Output envelope has no `computable` field; a non-nil pointer would " +
			"make a real empty workspace read as a refusal")
	}
}

// Helper-internal fields must not reach contract.Node. This is enforced by
// contract.Node having no home for them, and the test pins that: the
// Architext-facing contract sets additionalProperties:false, so any of these
// leaking through would fail validation on day one.
func TestRustHelperInternalFieldsAreStripped(t *testing.T) {
	blob, err := json.Marshal(contract.Node{ID: 1, Symbol: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"column", "bench", "trait_impl", "macro_truncated", "test_entry"} {
		if strings.Contains(string(blob), `"`+forbidden+`"`) {
			t.Errorf("contract.Node must not carry helper-internal field %q", forbidden)
		}
	}
}

func mustUnmarshal(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}
