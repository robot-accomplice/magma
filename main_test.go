package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

// prepareOutput accepts a single safe component and rejects anything that isn't
// one — the containment guard for untrusted names.
func TestPrepareOutput(t *testing.T) {
	root := t.TempDir()
	bad := []string{"", ".", "..", "a/b", `a\b`, "../escape"}
	for _, name := range bad {
		if _, err := prepareOutput(name, root); err == nil {
			t.Errorf("prepareOutput(%q) accepted an unsafe name", name)
		}
	}
	out, err := prepareOutput("proj", root)
	if err != nil {
		t.Fatalf("prepareOutput(valid): %v", err)
	}
	if filepath.Dir(out) != root {
		t.Errorf("output %q not directly under root %q", out, root)
	}
	if fi, err := os.Stat(out); err != nil || !fi.IsDir() {
		t.Errorf("output dir not created: %v", err)
	}
}

// End to end on a real Go repo: run emits a computable graph and the derived
// views correctly name the dead function.
func TestRunHappyPath(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"go.mod":  "module tmpmod\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() { Live() }\n\nfunc Live() {}\n\nfunc Dead() {}\n",
	})
	outRoot := t.TempDir()
	if err := run(repo, "proj", outRoot, false); err != nil {
		t.Fatalf("run: %v", err)
	}

	g := readGraph(t, filepath.Join(outRoot, "proj", "graph.json"))
	if !g.Computable {
		t.Fatalf("graph should be computable, got: %s", g.NotComputableReason)
	}

	dead := readNote(t, filepath.Join(outRoot, "proj", "_dead.json"))
	if !dead.ReachabilityComputable {
		t.Fatal("_dead should be computable for a repo with a main")
	}
	if !hasSymbol(dead, "Dead") {
		t.Errorf("_dead should list Dead, got %+v", dead.Rows)
	}
	if hasSymbol(dead, "Live") {
		t.Error("_dead must not list the reached function Live")
	}
}

// An unknown-language repo must produce an explicit refusal (not a missing file)
// AND surface a non-nil error to the caller so the exit code is non-zero.
func TestRunUnknownLanguageRefuses(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "no build manifest here\n"})
	outRoot := t.TempDir()

	err := run(repo, "proj", outRoot, false)
	if err == nil {
		t.Error("run on an unknown-language repo must return an error")
	}

	g := readGraph(t, filepath.Join(outRoot, "proj", "graph.json"))
	if g.Computable {
		t.Error("graph for an unknown language must be refused (Computable=false)")
	}
	if g.NotComputableReason == "" {
		t.Error("refusal must carry a reason")
	}
	// The derived views must exist and refuse too — the gate reads a refusal, never a gap.
	note := readNote(t, filepath.Join(outRoot, "proj", "_dead.json"))
	if note.ReachabilityComputable {
		t.Error("_dead must be refused for an unknown language")
	}
}

// parseArgs pulls the optional flags (--force, --help, --version) out of the args
// in any position and reports the positionals verbatim.
func TestParseArgs(t *testing.T) {
	cases := []struct {
		args    []string
		force   bool
		help    bool
		version bool
		pos     []string
	}{
		{args: []string{"repo", "name", "out"}, pos: []string{"repo", "name", "out"}},
		{args: []string{"--force", "repo", "name", "out"}, force: true, pos: []string{"repo", "name", "out"}},
		{args: []string{"repo", "name", "out", "-f"}, force: true, pos: []string{"repo", "name", "out"}},
		{args: []string{"-h"}, help: true},
		{args: []string{"--help"}, help: true},
		{args: []string{"--version"}, version: true},
		{args: []string{"-V"}, version: true},
		{args: nil},
	}
	for _, c := range cases {
		o := parseArgs(c.args)
		if o.force != c.force || o.help != c.help || o.version != c.version {
			t.Errorf("parseArgs(%v) = {force:%v help:%v version:%v}, want {force:%v help:%v version:%v}",
				c.args, o.force, o.help, o.version, c.force, c.help, c.version)
		}
		if len(o.pos) != len(c.pos) {
			t.Fatalf("parseArgs(%v) pos = %v, want %v", c.args, o.pos, c.pos)
		}
		for i := range c.pos {
			if o.pos[i] != c.pos[i] {
				t.Errorf("parseArgs(%v) pos[%d] = %q, want %q", c.args, i, o.pos[i], c.pos[i])
			}
		}
	}
}

// The usage screen must show the version, explain every argument (not just name
// them), name the flags, and carry a runnable example — the gaps the minimal
// version left.
func TestUsageTextIsInformative(t *testing.T) {
	u := usageText()
	for _, want := range []string{
		version,       // what version am I running
		"<repo-path>", // the args, named
		"<folder-name>",
		"<vault-path>",
		"folder", // <folder-name> explained, not just named
		"vault",  // <vault-path> explained
		"--force", "--help", "--version",
		"EXAMPLE",    // a runnable example
		"graph.json", // what it produces
	} {
		if !strings.Contains(u, want) {
			t.Errorf("usage text missing %q", want)
		}
	}
}

// expandTilde expands a leading ~ or ~/ to the home dir — magma must do this
// itself because a path quoted for spaces (e.g. "~/Claude Vault/x") is never
// expanded by the shell.
func TestExpandTilde(t *testing.T) {
	home := "/home/jon"
	cases := []struct{ in, want string }{
		{"~", home},
		{"~/Claude Vault/codemap", home + "/Claude Vault/codemap"},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"", ""},
		{"~user/x", "~user/x"}, // only a bare ~ or ~/ expands, not ~user
	}
	for _, c := range cases {
		if got := expandTilde(c.in, home); got != c.want {
			t.Errorf("expandTilde(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// humanCount inserts thousands separators so big counts are readable.
func TestHumanCount(t *testing.T) {
	cases := map[int]string{0: "0", 5: "5", 105: "105", 1000: "1,000", 17346: "17,346", 48993: "48,993", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := humanCount(in); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", in, got, want)
		}
	}
}

// report is the end-of-run breakdown. A computable run shows counts; a refused
// view shows its reason (not a misleading 0); a refused graph shows the top-level
// refusal.
func TestReport(t *testing.T) {
	meta := contract.Meta{Generator: "magma/test", SHA: "abc123", Tree: "abc123", Fidelity: "rta"}

	t.Run("computable shows counts", func(t *testing.T) {
		g := contract.NewGraph(meta, "go", "rta")
		g.Tree = "abc123"
		g.Nodes = make([]contract.Node, 17)
		g.Edges = make([]contract.Edge, 48)
		dead := meta.Computed([]contract.Row{{Symbol: "A"}, {Symbol: "B"}})
		to := meta.Computed([]contract.Row{{Symbol: "C"}})
		r := report(styler{}, "roboticus", "/out/roboticus", g, dead, to)
		for _, want := range []string{"roboticus", "abc123", "17", "48", "dead", "2", "test-only", "1", "rta", "/out/roboticus"} {
			if !strings.Contains(r, want) {
				t.Errorf("report missing %q in:\n%s", want, r)
			}
		}
	})

	t.Run("refused view shows reason not zero", func(t *testing.T) {
		g := contract.NewGraph(meta, "go", "rta")
		g.Nodes = make([]contract.Node, 5)
		dead := meta.Refused("no production main in scope")
		to := meta.Refused("no production main in scope")
		r := report(styler{}, "lib", "/out/lib", g, dead, to)
		if !strings.Contains(r, "refused") || !strings.Contains(r, "no production main") {
			t.Errorf("refused view must show its reason, got:\n%s", r)
		}
	})

	t.Run("refused graph shows top-level refusal", func(t *testing.T) {
		g := contract.NewGraph(meta, "rust", "").Refuse("language \"rust\" not built yet")
		dead := meta.Refused("language \"rust\" not built yet")
		to := meta.Refused("language \"rust\" not built yet")
		r := report(styler{}, "proj", "/out/proj", g, dead, to)
		if !strings.Contains(r, "refused") || !strings.Contains(r, "rust") {
			t.Errorf("refused graph must show the refusal, got:\n%s", r)
		}
	})
}

// isFresh is the idempotency guard: an existing map is fresh only when all three
// files are present, the stamped tree matches the current CLEAN tree, and the
// generator (magma version) matches — so a dirty tree or a version bump forces a
// rebuild.
func TestIsFresh(t *testing.T) {
	const gen = "magma/0.1.0"
	cases := []struct {
		name      string
		storeTree string
		storeGen  string
		metaTree  string
		metaGen   string
		omitView  bool
		omitGraph bool
		wantFresh bool
	}{
		{name: "matching clean tree and generator", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, wantFresh: true},
		{name: "stale sha", storeTree: "old999", storeGen: gen, metaTree: "abc123", metaGen: gen, wantFresh: false},
		{name: "dirty current tree is never fresh", storeTree: "abc123-dirty", storeGen: gen, metaTree: "abc123-dirty", metaGen: gen, wantFresh: false},
		{name: "generator (version) bump forces rebuild", storeTree: "abc123", storeGen: "magma/0.0.9", metaTree: "abc123", metaGen: gen, wantFresh: false},
		{name: "missing derived view is not fresh", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, omitView: true, wantFresh: false},
		{name: "missing graph is not fresh", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, omitGraph: true, wantFresh: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := t.TempDir()
			if !c.omitGraph {
				writeJSONFile(t, filepath.Join(out, "graph.json"),
					map[string]any{"tree": c.storeTree, "generator": c.storeGen})
			}
			writeJSONFile(t, filepath.Join(out, "_dead.json"), map[string]any{"tree": c.storeTree})
			if !c.omitView {
				writeJSONFile(t, filepath.Join(out, "_test-only.json"), map[string]any{"tree": c.storeTree})
			}
			meta := contract.Meta{Tree: c.metaTree, Generator: c.metaGen}
			if got := isFresh(out, meta); got != c.wantFresh {
				t.Errorf("isFresh = %v, want %v", got, c.wantFresh)
			}
		})
	}
}

// A committed (clean) repo mapped twice must NOT be re-analyzed the second time,
// and --force must override that and rebuild. The sentinel proves which happened:
// a skipped run leaves the file untouched; a rebuild overwrites it.
func TestRunSkipsFreshMapAndForceRegenerates(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"go.mod":  "module tmpmod\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() { Live() }\n\nfunc Live() {}\n\nfunc Dead() {}\n",
	})
	outRoot := t.TempDir()
	graphPath := filepath.Join(outRoot, "proj", "graph.json")

	if err := run(repo, "proj", outRoot, false); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Inject a sentinel key while keeping tree+generator intact, so the map stays
	// "fresh" but a rebuild would be detectable by the sentinel's disappearance.
	injectSentinel(t, graphPath)

	if err := run(repo, "proj", outRoot, false); err != nil {
		t.Fatalf("second (fresh) run: %v", err)
	}
	if !hasSentinel(t, graphPath) {
		t.Error("fresh map was regenerated; expected the run to skip analysis")
	}

	if err := run(repo, "proj", outRoot, true); err != nil {
		t.Fatalf("forced run: %v", err)
	}
	if hasSentinel(t, graphPath) {
		t.Error("--force did not rebuild the map (sentinel survived)")
	}
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func injectSentinel(t *testing.T, path string) {
	t.Helper()
	var m map[string]any
	readJSON(t, path, &m)
	m["_sentinel"] = true
	writeJSONFile(t, path, m)
}

func hasSentinel(t *testing.T, path string) bool {
	t.Helper()
	var m map[string]any
	readJSON(t, path, &m)
	_, ok := m["_sentinel"]
	return ok
}

// gitRepo makes a committed git repo containing the given files.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "init")
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "user.name", "Test")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "init")
	return repo
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func readGraph(t *testing.T, path string) contract.Graph {
	t.Helper()
	var g contract.Graph
	readJSON(t, path, &g)
	return g
}

func readNote(t *testing.T, path string) contract.Note {
	t.Helper()
	var n contract.Note
	readJSON(t, path, &n)
	return n
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
}

func hasSymbol(n contract.Note, sym string) bool {
	for _, r := range n.Rows {
		if r.Symbol == sym {
			return true
		}
	}
	return false
}
