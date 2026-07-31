package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
)

func init() { register(detect.Rust, rustBackend{}) }

// helperEnv overrides helper discovery with an explicit path. Checked before
// PATH so a developer can point at a build tree without installing.
const helperEnv = "MAGMA_RUST_HELPER"

// helperBin is the executable looked up on PATH when helperEnv is unset.
const helperBin = "magma-rust-helper"

// helperContractVersion is the only helper wire format this backend reads. The
// helper stamps it on every envelope, refusals included; a mismatch is refused
// rather than parsed optimistically, because the fields this backend depends on
// (notably `root`/`test`, which drive reachability) could change meaning
// without changing shape.
const helperContractVersion = "magma-rust-helper/1"

// rustBackend builds the call graph for a Cargo workspace by shelling out to
// the rust-analyzer-backed helper in ./rust-helper and mapping its
// `magma-rust-helper/1` output onto the contract graph.
//
// UNLIKE THE GO BACKEND, THIS IS NOT IN-PROCESS. Go's analysis needs only a
// `go` toolchain; Rust's needs a separate binary that links rust-analyzer as a
// library, which cannot ship through `go install`. The helper is therefore
// located on PATH and its absence is a REFUSAL with an install hint, not an
// error and not a silent skip — a repo magma cannot map honestly gets an honest
// refusal, exactly as it does for a Go module with no entrypoint.
//
// fidelity = "semantic": edges come from rust-analyzer's own name resolution and
// type inference, so a resolved call lands on the impl rustc would select.
// Desugared forms (operators, `for`, `await`, format args, `?`'s error
// conversion, `Drop` at scope exit) are resolved from types rather than syntax
// and are emitted as `dynamic` — real over-approximations, never invented
// edges. Every imprecision in the helper fails toward "live", so a node this
// map calls dead is dead conservatively.
type rustBackend struct{}

func (rustBackend) Language() string { return "rust" }

func (rustBackend) BuildGraph(repo string, meta contract.Meta, progress Progress) (contract.Graph, error) {
	step := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	g := contract.NewGraph(meta, "rust", "semantic")

	// Cargo has no single identifier analogous to a go.mod module path — a
	// workspace is a set of independently-named crates — so the repo's own
	// directory name is used as the map's label. Crate identity is not lost:
	// every node carries its crate in `pkg`.
	g.Module = filepath.Base(filepath.Clean(repo))

	step("locating rust helper")
	bin, err := findRustHelper()
	if err != nil {
		return g.Refuse(err.Error()), nil
	}

	step("analyzing workspace (two cargo configs)")
	// Loading a large workspace has been measured at 769.8s for ONE of the two
	// configs, so the helper's stderr progress is forwarded live rather than
	// discarded — without it a magma run sits on a single label for ten-plus
	// minutes and is indistinguishable from a hang. stderr is also buffered,
	// because it is the only diagnostic available if the helper fails.
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, repo)
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(&stderr, &progressLines{fn: step})
	if err := cmd.Run(); err != nil {
		// The helper reserves nonzero exits for usage misuse; a data-driven
		// refusal exits 0 WITH a JSON envelope (its own EXIT_USAGE comment
		// documents the split, deliberately mirroring Go's). So a nonzero exit
		// here is a genuine failure to run, not an answer.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return g, fmt.Errorf("rust helper %s exited %d: %s", bin, ee.ExitCode(), stderr.String())
		}
		return g, fmt.Errorf("running rust helper %s: %w", bin, err)
	}
	out := stdout.Bytes()

	step("mapping helper output")
	var env helperEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return g, fmt.Errorf("parsing rust helper output: %w", err)
	}
	if env.ContractVersion != helperContractVersion {
		return g, fmt.Errorf(
			"rust helper speaks %q, this magma reads %q — install a matching helper",
			env.ContractVersion, helperContractVersion)
	}
	// Computable is present only on a refusal envelope; an Output has no such
	// field, so a nil pointer means the analysis succeeded. Distinguishing on
	// presence rather than on `functions == nil` keeps an empty-but-real
	// workspace (zero functions) from being read as a refusal.
	if env.Computable != nil && !*env.Computable {
		return g.Refuse(env.Reason), nil
	}

	nodes := make([]contract.Node, 0, len(env.Functions))
	for _, f := range env.Functions {
		nodes = append(nodes, contract.Node{
			ID:        int(f.ID),
			Symbol:    f.Symbol,
			Pkg:       f.Pkg,
			File:      f.File,
			Line:      int(f.Line),
			Kind:      f.Kind,
			Exported:  f.Exported,
			Test:      f.Test,
			Root:      f.Root,
			Generated: f.Generated,
			Signature: f.Signature.toContract(),
			Doc:       f.Doc,
		})
	}

	edges := make([]contract.Edge, 0, len(env.Calls))
	for _, c := range env.Calls {
		edges = append(edges, contract.Edge{
			From: int(c.From),
			To:   int(c.To),
			File: c.SiteFile,
			Line: int(c.SiteLine),
			Kind: c.Kind,
		})
	}

	// The helper's own answer, carried through rather than dropped: analysing a
	// Rust workspace runs build scripts and expands proc macros, which a
	// consumer is entitled to know without inferring it from the language.
	g.ExecutedTargetCode = env.ExecutedTargetCode

	step("analyzing reachability")
	markReachable(nodes, edges, env.Functions)
	assignFan(nodes, edges)

	g.Nodes = nodes
	g.Edges = edges
	return g, nil
}

// helperProgressPrefix marks a helper stderr line as a live progress label.
// Matching an explicit prefix rather than forwarding all of stderr keeps cargo
// noise and build-script output — which the helper genuinely executes — from
// being surfaced to the user as if it were magma's own progress.
const helperProgressPrefix = "PROGRESS "

// progressLines is an io.Writer that calls fn once per COMPLETE line carrying
// helperProgressPrefix. Buffering the tail matters: a Write boundary can fall
// mid-line, and reporting half a label (or reporting it twice) would be worse
// than not reporting it.
type progressLines struct {
	buf []byte
	fn  func(string)
}

func (w *progressLines) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(bytes.TrimSpace(w.buf[:i]))
		w.buf = w.buf[i+1:]
		if s, ok := strings.CutPrefix(line, helperProgressPrefix); ok && s != "" {
			w.fn(s)
		}
	}
	return len(p), nil
}

// findRustHelper resolves the helper binary, preferring an explicit
// MAGMA_RUST_HELPER over PATH. The error text is user-facing: it becomes the
// refusal reason verbatim, so it says what is missing AND how to fix it.
func findRustHelper() (string, error) {
	if p := os.Getenv(helperEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%q is not usable: %v", helperEnv, p, err)
		}
		return p, nil
	}
	p, err := exec.LookPath(helperBin)
	if err != nil {
		return "", fmt.Errorf(
			"the Rust backend needs the %s binary, which was not found on PATH; "+
				"build it from the magma repo (cargo install --path rust-helper) "+
				"or set %s to its location",
			helperBin, helperEnv)
	}
	return p, nil
}

// markReachable fills Reachable and ProdReachable by BFS over the edge set.
//
// This is magma's job, not the helper's: the helper is an extractor and its
// output type structurally cannot carry reachability (see the helper's
// model.rs). Go derives the same two fields from rta.Result rather than from
// contract.Edge, so there is nothing in internal/contract to reuse here.
//
// Two traversals, matching Go's two-load split and for the same reason:
//
//   - ProdReachable: from the helper's `root` set alone. `roots::mark` sets
//     `root` for PRODUCTION entry points only — a bin's `main`, or a lib's
//     publicly reachable API — and explicitly never for test functions, so
//     this set needs no further filtering.
//
//   - Reachable: production roots PLUS every `#[test]` and `#[bench]` entry
//     point. Those are called by the compiler-generated harness, so nothing
//     inside the graph calls them, exactly as nothing calls `main`.
//
// THE SEED MUST USE test_entry, NOT test. `test` is deliberately broader —
// it also covers `#[cfg(test)]` ancestry and `tests/`/`benches/` integration
// targets — so seeding on it would make every function merely living under a
// test module its own root, trivially reaching itself, and a genuinely dead
// test helper could never be reported. Seeding on `root` alone has the
// opposite failure: all test-only code reads dead. `test_entry` (the narrow
// literal-`#[test]` signal) is the only correct seed, and it exists on the
// wire for exactly this reason — see model::Function::test_entry.
func markReachable(nodes []contract.Node, edges []contract.Edge, fns []helperFunction) {
	adj := make(map[int][]int, len(nodes))
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	// Keyed by id rather than by slice position: ids come from the helper and
	// nothing here should assume they stay dense or index-aligned.
	harnessEntry := make(map[int]bool, len(fns))
	for _, f := range fns {
		if f.TestEntry || f.Bench {
			harnessEntry[int(f.ID)] = true
		}
	}

	var allRoots, prodRoots []int
	for _, n := range nodes {
		if n.Root {
			prodRoots = append(prodRoots, n.ID)
		}
		if n.Root || harnessEntry[n.ID] {
			allRoots = append(allRoots, n.ID)
		}
	}

	reachAll := bfs(adj, allRoots)
	reachProd := bfs(adj, prodRoots)
	for i := range nodes {
		nodes[i].Reachable = reachAll[nodes[i].ID]
		nodes[i].ProdReachable = reachProd[nodes[i].ID]
	}
}

// bfs returns the set of node ids reachable from seeds along adj. Roots are
// members of their own reachable set, matching Go, where a root is reachable by
// definition and contract.Node.IsDead relies on it.
func bfs(adj map[int][]int, seeds []int) map[int]bool {
	seen := make(map[int]bool, len(seeds))
	queue := make([]int, 0, len(seeds))
	for _, s := range seeds {
		if !seen[s] {
			seen[s] = true
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return seen
}

// helperEnvelope reads either shape the helper emits — Output or Refusal —
// since they share contract_version and executed_target_code and are told apart
// by the refusal-only `computable` field.
//
// FIELDS DELIBERATELY NOT READ: `column`, `bench` (folded into prod-root
// selection, see markReachable), `trait_impl`, and `macro_truncated` are
// helper-internal, serving the oracle harness rather than magma. contract.Node
// has no home for them, so the strip is enforced by the type rather than by
// convention — which matters because the Architext-facing contract sets
// additionalProperties:false and would reject any of them on day one.
type helperEnvelope struct {
	ContractVersion string `json:"contract_version"`
	// ExecutedTargetCode records that mapping this repo RAN its code: build
	// scripts executed and proc macros expanded. Read here so the fact is not
	// silently lost; it has no contract.Graph field yet, and adding one is a
	// two-sided change with Architext.
	ExecutedTargetCode bool `json:"executed_target_code"`
	// Computable is present only on a refusal envelope — see BuildGraph.
	Computable *bool            `json:"computable"`
	Reason     string           `json:"reason"`
	Functions  []helperFunction `json:"functions"`
	Calls      []helperCall     `json:"calls"`
}

type helperFunction struct {
	ID        uint32          `json:"id"`
	Symbol    string          `json:"symbol"`
	Pkg       string          `json:"pkg"`
	File      string          `json:"file"`
	Line      uint32          `json:"line"`
	Kind      string          `json:"kind"`
	Exported  bool            `json:"exported"`
	Test      bool            `json:"test"`
	TestEntry bool            `json:"test_entry"`
	Root      bool            `json:"root"`
	Bench     bool            `json:"bench"`
	Generated bool            `json:"generated"`
	Signature helperSignature `json:"signature"`
	Doc       string          `json:"doc"`
}

type helperCall struct {
	From     uint32 `json:"from"`
	To       uint32 `json:"to"`
	SiteFile string `json:"site_file"`
	SiteLine uint32 `json:"site_line"`
	Kind     string `json:"kind"`
}

type helperSignature struct {
	Params  []helperParam  `json:"params"`
	Results []helperResult `json:"results"`
}

type helperParam struct {
	Name *string `json:"name"`
	Ty   string  `json:"ty"`
}

type helperResult struct {
	Ty string `json:"ty"`
}

func (s helperSignature) toContract() *contract.Signature {
	out := &contract.Signature{}
	for _, p := range s.Params {
		name := ""
		if p.Name != nil {
			name = *p.Name
		}
		out.Params = append(out.Params, contract.Param{Name: name, Type: p.Ty})
	}
	for _, r := range s.Results {
		out.Results = append(out.Results, contract.Result{Type: r.Ty})
	}
	return out
}
