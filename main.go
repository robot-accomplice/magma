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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
		fmt.Fprint(os.Stderr, usageText()) // misuse prints the help to stderr, exit 2
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
		"  magma [--force] <repo-path> <folder-name> <vault-path>\n\n" +
		"ARGUMENTS:\n" +
		"  <repo-path>     path to the Git repository to analyze (language is auto-detected;\n" +
		"                  v" + version + " supports Go — others are refused honestly)\n" +
		"  <folder-name>   a label for this map, and the folder created for it inside the\n" +
		"                  vault. Must be a single path component (no '/', '\\', '.', '..')\n" +
		"  <vault-path>    the vault directory the map folder is written into, as\n" +
		"                  <vault-path>/<folder-name>/ — graph.json, _dead.json, _test-only.json\n\n" +
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
		// Still emit the (refused) artifacts and the panel, but exit non-zero so
		// the audit gate and shells see the refusal.
		g := refusedGraph(meta, lang)
		if err := emitReport(out, meta, name, g); err != nil {
			return err
		}
		return fmt.Errorf("%s", g.NotComputableReason)
	}

	prog, clearProg := newProgress()
	g, err := b.BuildGraph(repo, meta, prog)
	clearProg()
	if err != nil {
		return err
	}
	return emitReport(out, meta, name, g)
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

// refusedGraph builds an explicit, machine-readable refusal when no backend
// exists for the detected language — the audit gate must see a refusal, never a
// missing file it could read as "nothing to report".
func refusedGraph(meta contract.Meta, lang detect.Lang) contract.Graph {
	var reason string
	if lang == detect.Unknown {
		reason = "no supported language detected (no go.mod, Cargo.toml, package.json, Gradle, or pom.xml)"
	} else {
		reason = fmt.Sprintf("language %q detected but its parser is not built yet (magma %s supports Go)", lang, version)
	}
	return contract.NewGraph(meta, string(lang), "").Refuse(reason)
}

// emitReport writes the three artifacts and prints the styled panel to stdout.
func emitReport(out string, meta contract.Meta, name string, g contract.Graph) error {
	dead, testOnly, err := writeArtifacts(out, meta, g)
	if err != nil {
		return err
	}
	fmt.Print(report(newStyler(os.Stdout), name, out, g, dead, testOnly))
	return nil
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

// ---- presentation ----

// styler applies ANSI SGR codes, but only when enabled (a real terminal with
// NO_COLOR unset). Disabled, it returns text unchanged, so piped/CI output and
// tests stay plain — the zero value is disabled.
type styler struct{ on bool }

func newStyler(f *os.File) styler {
	return styler{on: isTerminal(f) && os.Getenv("NO_COLOR") == ""}
}

func (s styler) paint(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

// ANSI SGR codes used in the report.
const (
	cBold   = "1"
	cDim    = "2"
	cRed    = "31"
	cGreen  = "32"
	cYellow = "33"
	cCyan   = "36"
)

// vlen is the display width of s in columns (runes), used for box alignment.
func vlen(s string) int { return utf8.RuneCountInString(s) }

// humanCount formats an int with thousands separators (17346 -> "17,346").
func humanCount(n int) string {
	s := strconv.Itoa(n)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return neg + string(out)
}

// report renders the end-of-run breakdown as a bordered panel. Counts for a
// computable run; a refused view or graph shows its reason (never a misleading 0).
func report(s styler, name, out string, g contract.Graph, dead, testOnly contract.Note) string {
	lang := g.Language
	if lang == "" {
		lang = "none"
	}
	rightHdr := lang + " · " + g.Tree

	type row struct{ label, value, code string }
	var rows []row
	var notes []string

	if !g.Computable {
		rows = append(rows, row{"status", "refused", cRed})
		notes = append(notes, g.NotComputableReason)
	} else {
		deadVal, deadCode := viewCell(dead, func(c int) string {
			if c > 0 {
				return cYellow
			}
			return cGreen
		})
		toVal, toCode := viewCell(testOnly, func(int) string { return cDim })
		rows = []row{
			{"nodes", humanCount(len(g.Nodes)), ""},
			{"edges", humanCount(len(g.Edges)), ""},
			{"dead code", deadVal, deadCode},
			{"test-only", toVal, toCode},
			{"fidelity", g.Fidelity, cCyan},
		}
		if r := refusalReason(dead, testOnly); r != "" {
			notes = append(notes, "reachability views refused: "+r)
		}
	}

	// inner is the visible width between the box's single-space side padding.
	// Width is counted in runes, not bytes — the middle dot in the header and any
	// non-ASCII symbol are one display column, not their UTF-8 byte length.
	inner := vlen(name) + vlen(rightHdr)
	for _, r := range rows {
		if w := vlen(r.label) + vlen(r.value); w > inner {
			inner = w
		}
	}
	inner += 4 // minimum gap between the left label and the right value
	if inner < 30 {
		inner = 30
	}

	// justify lays a left and right token across `inner` visible columns, then
	// colors each — padding is computed on the plain text so the color codes
	// (zero display width) never break alignment.
	justify := func(left, lcode, right, rcode string) string {
		gap := inner - vlen(left) - vlen(right)
		if gap < 1 {
			gap = 1
		}
		return s.paint(lcode, left) + strings.Repeat(" ", gap) + s.paint(rcode, right)
	}
	dash := func(n int) string { return strings.Repeat("─", n) }
	boxRow := func(content string) string {
		return s.paint(cDim, "│ ") + content + s.paint(cDim, " │")
	}

	var b strings.Builder
	// Titled top border: ╭─ magma ───╮
	fmt.Fprintln(&b, s.paint(cDim, "╭─ ")+s.paint(cCyan, "magma")+s.paint(cDim, " "+dash(inner-6)+"╮"))
	fmt.Fprintln(&b, boxRow(justify(name, cBold, rightHdr, cDim)))
	fmt.Fprintln(&b, s.paint(cDim, "├"+dash(inner+2)+"┤"))
	for _, r := range rows {
		fmt.Fprintln(&b, boxRow(justify(r.label, cDim, r.value, r.code)))
	}
	fmt.Fprintln(&b, s.paint(cDim, "╰"+dash(inner+2)+"╯"))
	fmt.Fprintln(&b, s.paint(cDim, "→ ")+out)
	for _, n := range notes {
		fmt.Fprintln(&b, s.paint(cDim, "  "+n))
	}
	return b.String()
}

// viewCell renders a view's row count coloured by `color`, or a red "refused"
// so a refused view is never mistaken for "found nothing".
func viewCell(n contract.Note, color func(int) string) (value, code string) {
	if !n.ReachabilityComputable {
		return "refused", cRed
	}
	c := len(n.Rows)
	return humanCount(c), color(c)
}

// refusalReason returns the reason a view could not be computed, if any.
func refusalReason(dead, testOnly contract.Note) string {
	if !dead.ReachabilityComputable {
		return dead.NotComputableReason
	}
	if !testOnly.ReachabilityComputable {
		return testOnly.NotComputableReason
	}
	return ""
}

// newProgress returns a stage reporter backed by an animated spinner on its own
// goroutine (so the line keeps moving during a multi-second phase), plus a cleanup
// that stops it and erases the line. When stderr is not a terminal (pipe, CI) it
// reports nothing, so logs and captured output stay clean.
func newProgress() (backend.Progress, func()) {
	if !isTerminal(os.Stderr) {
		return nil, func() {}
	}
	s := newStyler(os.Stderr)
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	var mu sync.Mutex
	stage := "starting"
	stop, done := make(chan struct{}), make(chan struct{})
	ticker := time.NewTicker(80 * time.Millisecond)

	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-ticker.C:
				mu.Lock()
				st := stage
				mu.Unlock()
				fmt.Fprintf(os.Stderr, "\r%s %s\033[K",
					s.paint(cCyan, string(frames[i%len(frames)])), s.paint(cDim, st))
			}
		}
	}()

	progress := func(st string) { mu.Lock(); stage = st; mu.Unlock() }
	clear := func() {
		ticker.Stop()
		close(stop)
		<-done
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	return progress, clear
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
