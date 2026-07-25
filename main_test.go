package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/architext"
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
	if err := run(repo, "proj", outRoot, runOpts{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	g := readGraph(t, filepath.Join(outRoot, "proj", ".magma", "graph.json"))
	if !g.Computable {
		t.Fatalf("graph should be computable, got: %s", g.NotComputableReason)
	}

	dead := readNote(t, filepath.Join(outRoot, "proj", ".magma", "_dead.json"))
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

	err := run(repo, "proj", outRoot, runOpts{})
	if err == nil {
		t.Error("run on an unknown-language repo must return an error")
	}

	g := readGraph(t, filepath.Join(outRoot, "proj", ".magma", "graph.json"))
	if g.Computable {
		t.Error("graph for an unknown language must be refused (Computable=false)")
	}
	if g.NotComputableReason == "" {
		t.Error("refusal must carry a reason")
	}
	// The derived views must exist and refuse too — the gate reads a refusal, never a gap.
	note := readNote(t, filepath.Join(outRoot, "proj", ".magma", "_dead.json"))
	if note.ReachabilityComputable {
		t.Error("_dead must be refused for an unknown language")
	}
}

// A refused map (Computable=false) must never be reported as "already fresh" on
// a second run — isFresh's tree/generator/manifest checks all match for a
// stored refusal, but a refusal must always re-attempt, so run must keep
// returning a non-nil error every time, not silently succeed on rerun.
func TestRunRefusalIsNeverFresh(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "x\n"})
	outRoot := t.TempDir()

	if err := run(repo, "proj", outRoot, runOpts{}); err == nil {
		t.Fatal("first run on an unsupported-language repo must return an error")
	}

	if err := run(repo, "proj", outRoot, runOpts{}); err == nil {
		t.Error("second run must re-attempt and refuse again, not report 'already fresh'")
	}
}

// run must write the Obsidian markdown map alongside the JSON, and the JSON
// must live under the hidden .magma/ subfolder, not the folder root.
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

// Regenerating a map must delete a note the previous run wrote but the new
// render no longer lists (a stale note left behind by a code change), while
// leaving alone any note a human wrote by hand that magma never generated.
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

// safeJoin (the containment guard behind writeContained/removeContained) must
// refuse a path that passes through a symlink planted at any component, not
// just resolve it and follow along. relPath's first component ("nodes") is
// itself a symlink pointing at a directory OUTSIDE out; a no-op guard would
// happily write/delete through it into that external directory.
func TestSafeJoinRejectsSymlinkEscape(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(out, "nodes")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join("nodes", "x.md")

	if err := writeContained(out, target, []byte("pwned")); err == nil {
		t.Error("writeContained through a symlinked path component must be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.md")); !os.IsNotExist(err) {
		t.Error("writeContained must not have written through the symlink into the external target")
	}

	// Plant the file directly in the external dir (bypassing the guard) so a
	// buggy removeContained that follows the symlink has something to delete —
	// proving the assertion is genuinely exercising the check, not vacuously
	// passing because there was nothing there to remove.
	external := filepath.Join(outside, "x.md")
	if err := os.WriteFile(external, []byte("do not delete me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeContained(out, target); err == nil {
		t.Error("removeContained through a symlinked path component must be rejected")
	}
	if _, err := os.Stat(external); err != nil {
		t.Error("removeContained must not have deleted through the symlink into the external target")
	}
}

// A relative path containing ".." must never resolve outside out, regardless
// of whether any component is a symlink.
func TestSafeJoinRejectsTraversal(t *testing.T) {
	out := t.TempDir()
	escapedTarget := filepath.Join(filepath.Dir(out), "escape.md")

	if err := writeContained(out, filepath.Join("..", "escape.md"), []byte("pwned")); err == nil {
		t.Error("writeContained must reject a relative path that escapes out via ..")
	}
	if _, err := os.Stat(escapedTarget); !os.IsNotExist(err) {
		t.Error("writeContained must not have written outside out")
	}
}

// isFresh must require the FULL markdown asset set the manifest promises, not
// just the JSON. We delete "Dead code.md" rather than Overview.md: the
// fresh-skip path always re-renders Overview.md regardless (its "Last
// validated" stamp legitimately changes on every run), so deleting it wouldn't
// distinguish a genuine full rebuild from the bug this guards against — a
// deleted index/function note being silently accepted as still "fresh".
func TestRunNotFreshWhenMarkdownMissing(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"go.mod":  "module tmpmod\n\ngo 1.21\n",
		"main.go": "package main\nfunc main(){ Live() }\nfunc Live(){}\nfunc Dead(){}\n",
	})
	outRoot := t.TempDir()
	base := filepath.Join(outRoot, "proj")
	if err := run(repo, "proj", outRoot, runOpts{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	deadIndex := filepath.Join(base, "Dead code.md")
	if err := os.Remove(deadIndex); err != nil {
		t.Fatal(err)
	}
	if err := run(repo, "proj", outRoot, runOpts{}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, err := os.Stat(deadIndex); err != nil {
		t.Errorf("Dead code.md missing after rerun; isFresh must rebuild when the manifest's markdown set is incomplete: %v", err)
	}
}

// parseArgs pulls the optional flags (--force, --help, --version, --depth,
// --from, --hotspots, --graph-link) out of the args in any position and
// reports the positionals verbatim.
func TestParseArgs(t *testing.T) {
	cases := []struct {
		args         []string
		force        bool
		help         bool
		version      bool
		depth        int
		hotspots     int
		from         string
		graphLink    string
		graphLinkSet bool
		pos          []string
	}{
		{args: []string{"repo", "name", "out"}, pos: []string{"repo", "name", "out"}},
		{args: []string{"--force", "repo", "name", "out"}, force: true, pos: []string{"repo", "name", "out"}},
		{args: []string{"repo", "name", "out", "-f"}, force: true, pos: []string{"repo", "name", "out"}},
		{args: []string{"-h"}, help: true},
		{args: []string{"--help"}, help: true},
		{args: []string{"--version"}, version: true},
		{args: []string{"-V"}, version: true},
		{args: nil},
		{args: []string{"--depth", "2", "repo", "name", "out"}, depth: 2, pos: []string{"repo", "name", "out"}},
		{args: []string{"--from", "pkg.Fn", "repo", "name", "out"}, from: "pkg.Fn", pos: []string{"repo", "name", "out"}},
		{args: []string{"--hotspots", "5", "repo", "name", "out"}, hotspots: 5, pos: []string{"repo", "name", "out"}},
		{args: []string{"--graph-link", "repo", "name", "out"}, graphLinkSet: true, pos: []string{"repo", "name", "out"}},
		{args: []string{"--graph-link=MyVault", "repo", "name", "out"}, graphLinkSet: true, graphLink: "MyVault", pos: []string{"repo", "name", "out"}},
	}
	for _, c := range cases {
		o := parseArgs(c.args)
		if o.force != c.force || o.help != c.help || o.version != c.version ||
			o.depth != c.depth || o.hotspots != c.hotspots || o.from != c.from ||
			o.graphLink != c.graphLink || o.graphLinkSet != c.graphLinkSet {
			t.Errorf("parseArgs(%v) = %+v, want force:%v help:%v version:%v depth:%v hotspots:%v from:%q graphLink:%q graphLinkSet:%v",
				c.args, o, c.force, c.help, c.version, c.depth, c.hotspots, c.from, c.graphLink, c.graphLinkSet)
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

// --architext sets opts.architext; a bare flag leaves architextDir empty
// (falling back to the repo-relative default in run), while --architext=DIR
// captures the override.
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
		"--depth", "--from", "--hotspots", "--graph-link", // new walk/display flags
		"--architext", // code-graph emitter flag
		"EXAMPLE",     // a runnable example
		"graph.json",  // what it produces
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
// refusal. The panel gains a "notes" row and never shows "fidelity" — that field
// is a constant ("rta") today and noise to a human; it stays in the JSON.
func TestReport(t *testing.T) {
	meta := contract.Meta{Generator: "magma/test", SHA: "abc123", Tree: "abc123", Fidelity: "rta"}

	t.Run("computable shows counts", func(t *testing.T) {
		g := contract.NewGraph(meta, "go", "rta")
		g.Tree = "abc123"
		g.Nodes = make([]contract.Node, 17)
		g.Edges = make([]contract.Edge, 48)
		dead := meta.Computed([]contract.Row{{Symbol: "A"}, {Symbol: "B"}})
		to := meta.Computed([]contract.Row{{Symbol: "C"}})
		r := report(styler{}, "roboticus", "/out/roboticus", g, dead, to, 5)
		for _, want := range []string{"roboticus", "abc123", "17", "48", "dead", "2", "test-only", "1", "notes", "5", "/out/roboticus"} {
			if !strings.Contains(r, want) {
				t.Errorf("report missing %q in:\n%s", want, r)
			}
		}
		if strings.Contains(r, "fidelity") {
			t.Error("fidelity row must be removed from the human panel (it's noise; the field stays in the JSON)")
		}
	})

	t.Run("refused view shows reason not zero", func(t *testing.T) {
		g := contract.NewGraph(meta, "go", "rta")
		g.Nodes = make([]contract.Node, 5)
		dead := meta.Refused("no production main in scope")
		to := meta.Refused("no production main in scope")
		r := report(styler{}, "lib", "/out/lib", g, dead, to, 4)
		if !strings.Contains(r, "refused") || !strings.Contains(r, "no production main") {
			t.Errorf("refused view must show its reason, got:\n%s", r)
		}
	})

	t.Run("refused graph shows top-level refusal", func(t *testing.T) {
		g := contract.NewGraph(meta, "rust", "").Refuse("language \"rust\" not built yet")
		dead := meta.Refused("language \"rust\" not built yet")
		to := meta.Refused("language \"rust\" not built yet")
		r := report(styler{}, "proj", "/out/proj", g, dead, to, 1)
		if !strings.Contains(r, "refused") || !strings.Contains(r, "rust") {
			t.Errorf("refused graph must show the refusal, got:\n%s", r)
		}
	})
}

// isFresh is the idempotency guard: an existing map is fresh only when all
// three JSON files are present, the stamped tree matches the current CLEAN
// tree, the generator (magma version) matches, AND every markdown path the
// manifest lists is still present on disk — so a dirty tree, a version bump, a
// missing manifest (a JSON-only folder from an older run), or a manifest
// listing a deleted note all force a rebuild.
func TestIsFresh(t *testing.T) {
	const gen = "magma/0.1.0"
	cases := []struct {
		name         string
		storeTree    string
		storeGen     string
		metaTree     string
		metaGen      string
		omitView     bool
		omitGraph    bool
		omitManifest bool
		omitMarkdown bool
		storeRefused bool
		wantFresh    bool
	}{
		{name: "matching clean tree and generator", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, wantFresh: true},
		{name: "refused stored graph is never fresh, even with matching tree/generator", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, storeRefused: true, wantFresh: false},
		{name: "stale sha", storeTree: "old999", storeGen: gen, metaTree: "abc123", metaGen: gen, wantFresh: false},
		{name: "dirty current tree is never fresh", storeTree: "abc123-dirty", storeGen: gen, metaTree: "abc123-dirty", metaGen: gen, wantFresh: false},
		{name: "generator (version) bump forces rebuild", storeTree: "abc123", storeGen: "magma/0.0.9", metaTree: "abc123", metaGen: gen, wantFresh: false},
		{name: "missing derived view is not fresh", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, omitView: true, wantFresh: false},
		{name: "missing graph is not fresh", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, omitGraph: true, wantFresh: false},
		{name: "missing manifest (JSON-only folder) is not fresh", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, omitManifest: true, wantFresh: false},
		{name: "manifest listing a deleted markdown file is not fresh", storeTree: "abc123", storeGen: gen, metaTree: "abc123", metaGen: gen, omitMarkdown: true, wantFresh: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := t.TempDir()
			dataDir := filepath.Join(out, ".magma")
			if err := os.MkdirAll(dataDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if !c.omitGraph {
				writeJSONFile(t, filepath.Join(dataDir, "graph.json"),
					map[string]any{"tree": c.storeTree, "generator": c.storeGen, "computable": !c.storeRefused})
			}
			writeJSONFile(t, filepath.Join(dataDir, "_dead.json"), map[string]any{"tree": c.storeTree})
			if !c.omitView {
				writeJSONFile(t, filepath.Join(dataDir, "_test-only.json"), map[string]any{"tree": c.storeTree})
			}
			if !c.omitManifest {
				writeJSONFile(t, filepath.Join(dataDir, "manifest.json"), []string{"Overview.md"})
				if !c.omitMarkdown {
					if err := os.WriteFile(filepath.Join(out, "Overview.md"), []byte("x"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			meta := contract.Meta{Tree: c.metaTree, Generator: c.metaGen}
			if got := isFresh(out, meta, false); got != c.wantFresh {
				t.Errorf("isFresh = %v, want %v", got, c.wantFresh)
			}
		})
	}
}

// isFresh must rebuild (return false) when architext output was requested but
// the vault's .magma/code-graph.json mirror is missing, even though the other
// three JSON artifacts and the manifest are otherwise fresh — a map built
// before --architext was added must not be silently accepted as covering it.
func TestIsFreshRequiresArchitextWhenRequested(t *testing.T) {
	out := t.TempDir()
	dataDir := filepath.Join(out, ".magma")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(dataDir, "graph.json"),
		map[string]any{"tree": "abc123", "generator": "magma/0.1.0", "computable": true})
	writeJSONFile(t, filepath.Join(dataDir, "_dead.json"), map[string]any{"tree": "abc123"})
	writeJSONFile(t, filepath.Join(dataDir, "_test-only.json"), map[string]any{"tree": "abc123"})
	writeJSONFile(t, filepath.Join(dataDir, "manifest.json"), []string{"Overview.md"})
	if err := os.WriteFile(filepath.Join(out, "Overview.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := contract.Meta{Tree: "abc123", Generator: "magma/0.1.0"}

	if !isFresh(out, meta, false) {
		t.Error("map should be fresh when architext isn't requested")
	}
	if isFresh(out, meta, true) {
		t.Error("map must not be fresh when architext is requested but code-graph.json is missing")
	}

	if err := os.WriteFile(filepath.Join(dataDir, architext.FileName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isFresh(out, meta, true) {
		t.Error("map should be fresh once code-graph.json exists and architext is requested")
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
	graphPath := filepath.Join(outRoot, "proj", ".magma", "graph.json")

	if err := run(repo, "proj", outRoot, runOpts{}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Inject a sentinel key while keeping tree+generator intact, so the map stays
	// "fresh" but a rebuild would be detectable by the sentinel's disappearance.
	injectSentinel(t, graphPath)

	if err := run(repo, "proj", outRoot, runOpts{}); err != nil {
		t.Fatalf("second (fresh) run: %v", err)
	}
	if !hasSentinel(t, graphPath) {
		t.Error("fresh map was regenerated; expected the run to skip analysis")
	}

	if err := run(repo, "proj", outRoot, runOpts{force: true}); err != nil {
		t.Fatalf("forced run: %v", err)
	}
	if hasSentinel(t, graphPath) {
		t.Error("--force did not rebuild the map (sentinel survived)")
	}
}

// --architext (via runOpts.architext) must dual-write code-graph.json to the
// repo's docs/architext/data/ AND the vault's hidden .magma/ mirror, for a
// computable graph.
func TestRunArchitextFlagEmitsCodeGraph(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"go.mod":  "module tmpmod\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() { Live() }\n\nfunc Live() {}\n\nfunc Dead() {}\n",
	})
	outRoot := t.TempDir()
	if err := run(repo, "proj", outRoot, runOpts{architext: true}); err != nil {
		t.Fatalf("run: %v", err)
	}

	repoDest := filepath.Join(repo, "docs", "architext", "data", architext.FileName)
	vaultDest := filepath.Join(outRoot, "proj", ".magma", architext.FileName)
	for _, p := range []string{repoDest, vaultDest} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		if !strings.Contains(string(b), `"contract_version": "magma-code-graph/1"`) {
			t.Errorf("%s missing contract_version stamp:\n%s", p, b)
		}
	}
}

// Emission must also happen on the refusal path (unsupported language): an
// honest refused code-graph is required, mirroring how the other refused
// artifacts (graph.json, _dead.json, ...) are always written even on refusal.
func TestRunArchitextFlagEmitsOnRefusal(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "no build manifest here\n"})
	outRoot := t.TempDir()

	if err := run(repo, "proj", outRoot, runOpts{architext: true}); err == nil {
		t.Fatal("run on an unknown-language repo must return an error")
	}

	repoDest := filepath.Join(repo, "docs", "architext", "data", architext.FileName)
	vaultDest := filepath.Join(outRoot, "proj", ".magma", architext.FileName)
	for _, p := range []string{repoDest, vaultDest} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		if !strings.Contains(string(b), `"contract_version": "magma-code-graph/1"`) {
			t.Errorf("%s missing contract_version stamp:\n%s", p, b)
		}
		if !strings.Contains(string(b), `"computable": false`) {
			t.Errorf("%s should be a refused code-graph (computable:false), got:\n%s", p, b)
		}
	}
}

// The artifact --architext writes into the repo (docs/architext/data/) is
// untracked on disk, so a naive dirty check would see it on the SECOND run and
// stamp tree: "<sha>-dirty" — flipping the otherwise-deterministic envelope.
// Running twice against a clean, committed fixture must produce byte-identical
// repo-side code-graph.json output both times.
func TestArchitextEmissionIsDeterministicAcrossRuns(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"go.mod":  "module tmpmod\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() { Live() }\n\nfunc Live() {}\n\nfunc Dead() {}\n",
	})
	outRoot := t.TempDir()
	repoDest := filepath.Join(repo, "docs", "architext", "data", architext.FileName)

	if err := run(repo, "proj", outRoot, runOpts{architext: true}); err != nil {
		t.Fatalf("run (first): %v", err)
	}
	first, err := os.ReadFile(repoDest)
	if err != nil {
		t.Fatalf("reading %s after first run: %v", repoDest, err)
	}

	if err := run(repo, "proj", outRoot, runOpts{architext: true}); err != nil {
		t.Fatalf("run (second): %v", err)
	}
	second, err := os.ReadFile(repoDest)
	if err != nil {
		t.Fatalf("reading %s after second run: %v", repoDest, err)
	}

	if !bytes.Equal(first, second) {
		t.Errorf("two --architext runs produced different bytes:\n--- first ---\n%s\n--- second ---\n%s", first, second)
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
