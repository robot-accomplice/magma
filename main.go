// Command magma extracts a repository's call graph — the map of which
// functions call which — and derives reachability views (dead code, test-only
// code) from it. It is deterministic and LLM-free: given a repo and a SHA it
// produces the same graph every time, so it runs as a tokenless prerequisite to
// any code audit, in CI, or as a git hook.
//
// Each language is analyzed by a bundled library that does the heavy lifting
// (Go: x/tools packages+ssa+rta, in-process, needing only `go`), never by a
// third-party analyzer the user must install. A language whose parser is not
// yet built refuses honestly rather than faking a map.
//
// Releases are gated by language support: v0.1.x is complete Go support.
//
// Usage:
//
//	magma <repo-path> <name> <output-root>
//
// Writes <output-root>/<name>/graph.json (the call graph) plus derived
// _dead.json and _test-only.json (the codemap-rows/1 contract the audit gate
// consumes).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/robot-accomplice/magma/internal/backend"
	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
	"github.com/robot-accomplice/magma/internal/gitmeta"
)

// version tracks language support, not the data contract: 0.1.x == complete Go.
const version = "0.1.0"

func main() {
	opt := parseArgs(os.Args[1:])
	switch {
	case opt.help:
		fmt.Print(usageText()) // asked-for help goes to stdout, exit 0
		return
	case opt.version:
		fmt.Println("magma " + version)
		return
	case len(opt.pos) != 3:
		fmt.Fprint(os.Stderr, usageText()) // misuse goes to stderr, exit 2
		os.Exit(2)
	}
	if err := run(opt.pos[0], opt.pos[1], opt.pos[2], opt.force); err != nil {
		fmt.Fprintln(os.Stderr, "magma: "+err.Error())
		os.Exit(1)
	}
}

// options is the parsed command line: the flags plus the leftover positionals.
type options struct {
	force   bool
	help    bool
	version bool
	pos     []string
}

// parseArgs splits the flags (--force/-f, --help/-h, --version/-V) from the three
// positional args, accepting flags in any position. Freshness skipping is the
// default; --force rebuilds an already-fresh map.
func parseArgs(args []string) options {
	var o options
	for _, a := range args {
		switch a {
		case "--force", "-f":
			o.force = true
		case "--help", "-h":
			o.help = true
		case "--version", "-V":
			o.version = true
		default:
			o.pos = append(o.pos, a)
		}
	}
	return o
}

// usageText is the help/usage screen: what magma is, what each argument means
// (not just its name), the flags, and a runnable example.
func usageText() string {
	return "magma " + version + " — deterministic call-graph & reachability mapper (Go)\n\n" +
		"Extracts a repository's call graph (functions/methods = nodes, calls = edges) and\n" +
		"derives reachability views (dead code, test-only code). Deterministic and LLM-free:\n" +
		"same repo + SHA → same graph. Skips the rebuild when a fresh map already exists.\n\n" +
		"USAGE:\n" +
		"  magma [--force] <repo-path> <name> <output-root>\n\n" +
		"ARGUMENTS:\n" +
		"  <repo-path>     path to the Git repository to analyze (language is auto-detected;\n" +
		"                  v" + version + " supports Go — others are refused honestly)\n" +
		"  <name>          a label for this map, and the subdirectory it is written to under\n" +
		"                  <output-root>. Must be a single path component (no '/', '\\', '.', '..')\n" +
		"  <output-root>   directory the map is written under, as <output-root>/<name>/,\n" +
		"                  producing graph.json, _dead.json and _test-only.json\n\n" +
		"FLAGS:\n" +
		"  -f, --force     rebuild even when a fresh map already exists for this commit\n" +
		"  -h, --help      show this help and exit\n" +
		"  -V, --version   print the version and exit\n\n" +
		"EXAMPLE:\n" +
		"  magma ~/code/roboticus roboticus ~/maps\n" +
		"  # writes ~/maps/roboticus/{graph.json,_dead.json,_test-only.json}\n\n" +
		"Re-run at an audit's frozen HEAD; a 'tree' ending in '-dirty' means the working tree\n" +
		"wasn't clean, so the map can't be reproduced from its SHA.\n"
}

func run(repoArg, name, outRoot string, force bool) error {
	// Expand a leading ~ ourselves: a path quoted for spaces (e.g. "~/Claude
	// Vault/x") is never expanded by the shell, and silently writing under cwd/~
	// would be a data-in-the-wrong-place bug.
	home, _ := os.UserHomeDir()
	repoArg = expandTilde(repoArg, home)
	outRoot = expandTilde(outRoot, home)

	out, err := prepareOutput(name, outRoot)
	if err != nil {
		return err
	}
	repo, err := filepath.Abs(repoArg)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(repo); err != nil || !fi.IsDir() {
		return fmt.Errorf("repo %q is not a directory", repoArg)
	}

	meta, err := gitmeta.Load(repo)
	if err != nil {
		return fmt.Errorf("reading git metadata (is %q a git repo?): %w", repoArg, err)
	}
	meta.Generator = "magma/" + version

	// Building the map is the expensive step, so skip it when a current one is
	// already on disk — magma is meant to be run before every audit/code task and
	// must be near-free when nothing changed. --force overrides.
	if !force && isFresh(out, meta) {
		fmt.Printf("magma: %s [%s] already fresh -> %s (use --force to rebuild)\n", name, meta.Tree, out)
		return nil
	}

	lang := detect.Detect(repo)
	b, ok := backend.For(lang)
	if !ok {
		return writeRefusal(out, meta, lang)
	}

	prog, clearProg := newProgress()
	g, err := b.BuildGraph(repo, meta, prog)
	if err != nil {
		clearProg()
		return err
	}
	if prog != nil {
		prog("writing map")
	}
	dead, testOnly, err := writeArtifacts(out, meta, g)
	clearProg()
	if err != nil {
		return err
	}
	fmt.Print(report(name, out, g, dead, testOnly))
	return nil
}

// isFresh reports whether a complete, current map already exists at out — the
// idempotency guard that lets magma run before every task for near-zero cost.
// Fresh means: all three files present, graph.json stamps the exact tree we're
// looking at, and it was built by THIS magma version. A dirty tree is never fresh
// (its working-tree content can't be reproduced from the sha), and a generator
// mismatch means a magma upgrade must re-map even at the same commit.
func isFresh(out string, meta contract.Meta) bool {
	if strings.HasSuffix(meta.Tree, "-dirty") {
		return false
	}
	for _, f := range []string{"graph.json", "_dead.json", "_test-only.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			return false
		}
	}
	b, err := os.ReadFile(filepath.Join(out, "graph.json"))
	if err != nil {
		return false
	}
	var g contract.Graph
	if err := json.Unmarshal(b, &g); err != nil {
		return false
	}
	return g.Tree == meta.Tree && g.Generator == meta.Generator
}

// prepareOutput validates name as a single safe path component, resolves the
// output directory strictly under outRoot (no traversal, no symlink escape),
// and creates it.
func prepareOutput(name, outRoot string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("name %q must be a single path component (no /, \\, . or ..)", name)
	}
	root, err := filepath.Abs(outRoot)
	if err != nil {
		return "", err
	}
	out := filepath.Join(root, name)
	if filepath.Dir(out) != root {
		return "", fmt.Errorf("output path %q escapes root %q", out, root)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	return out, nil
}

// expandTilde expands a leading ~ or ~/ to home. magma does this itself because
// a path quoted to survive spaces is never expanded by the shell. Only a bare ~
// or ~/... expands — ~user is left untouched.
func expandTilde(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// writeRefusal records an explicit, machine-readable refusal when no backend
// exists for the detected language — the audit gate must see a refusal, never a
// missing file it could read as "nothing to report".
func writeRefusal(out string, meta contract.Meta, lang detect.Lang) error {
	var reason string
	if lang == detect.Unknown {
		reason = "no supported language detected (no go.mod, Cargo.toml, package.json, Gradle, or pom.xml)"
	} else {
		reason = fmt.Sprintf("language %q detected but its parser is not built yet (magma %s supports Go)", lang, version)
	}
	g := contract.NewGraph(meta, string(lang), "").Refuse(reason)
	if _, _, err := writeArtifacts(out, meta, g); err != nil {
		return err
	}
	return fmt.Errorf("%s", reason)
}

// writeArtifacts emits the graph and its two derived views together (so the three
// artifacts always agree on computability) and returns the two views so the caller
// can report on them without re-deriving.
func writeArtifacts(out string, meta contract.Meta, g contract.Graph) (dead, testOnly contract.Note, err error) {
	dead = g.DeadView(meta)
	testOnly = g.TestOnlyView(meta)
	if err = contract.WriteGraph(out, g); err != nil {
		return dead, testOnly, err
	}
	if err = contract.Write(out, "_dead", dead); err != nil {
		return dead, testOnly, err
	}
	err = contract.Write(out, "_test-only", testOnly)
	return dead, testOnly, err
}

// report is the end-of-run breakdown printed to stdout: counts for a computable
// run, and the refusal reason (never a misleading 0) for anything that could not
// be computed.
func report(name, out string, g contract.Graph, dead, testOnly contract.Note) string {
	lang := g.Language
	if lang == "" {
		lang = "none"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "magma: %s [%s @ %s] -> %s\n", name, lang, g.Tree, out)
	if !g.Computable {
		fmt.Fprintf(&b, "  refused: %s\n", g.NotComputableReason)
		return b.String()
	}
	fmt.Fprintf(&b, "  nodes      %8d\n", len(g.Nodes))
	fmt.Fprintf(&b, "  edges      %8d\n", len(g.Edges))
	fmt.Fprintf(&b, "  dead code  %s\n", viewCount(dead))
	fmt.Fprintf(&b, "  test-only  %s\n", viewCount(testOnly))
	fmt.Fprintf(&b, "  fidelity   %8s\n", g.Fidelity)
	return b.String()
}

// viewCount renders a view's row count, or its refusal reason so a refused view
// is never mistaken for "found nothing".
func viewCount(n contract.Note) string {
	if !n.ReachabilityComputable {
		return "refused (" + n.NotComputableReason + ")"
	}
	return fmt.Sprintf("%8d", len(n.Rows))
}

// newProgress returns a stage reporter that overwrites a single stderr line, plus
// a cleanup that erases it. When stderr is not a terminal (pipe, CI) it reports
// nothing, so logs and captured output stay clean.
func newProgress() (backend.Progress, func()) {
	if !isTerminal(os.Stderr) {
		return nil, func() {}
	}
	p := func(stage string) {
		fmt.Fprintf(os.Stderr, "\r  %s…\033[K", stage) // \033[K clears to end of line
	}
	clear := func() { fmt.Fprint(os.Stderr, "\r\033[K") }
	return p, clear
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
