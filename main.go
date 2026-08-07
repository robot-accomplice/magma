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
// Writes an Obsidian markdown map (Overview.md, per-function notes, ...) to
// <output-root>/<name>/, plus the codemap-graph/1 and codemap-rows/1 JSON
// contracts (graph.json, _dead.json, _test-only.json, manifest.json) hidden
// under <output-root>/<name>/.magma/ — the audit gate reads those directly.
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

	"github.com/robot-accomplice/magma/internal/architext"
	"github.com/robot-accomplice/magma/internal/backend"
	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
	"github.com/robot-accomplice/magma/internal/gitmeta"
	"github.com/robot-accomplice/magma/internal/notes"
)

// version tracks language support, not the data contract: 0.1.x == complete Go.
const version = "0.3.0"

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

	// A bare --graph-link (no explicit vault) defaults to the vault-path's own
	// folder name — the one piece of information magma already has that, in the
	// common case (vault-path IS the Obsidian vault), is the vault's name.
	graphLink := opt.graphLink
	if opt.graphLinkSet && graphLink == "" {
		home, _ := os.UserHomeDir()
		graphLink = filepath.Base(expandTilde(opt.pos[2], home))
	}

	ro := runOpts{
		force:        opt.force,
		depth:        opt.depth,
		hotspots:     opt.hotspots,
		from:         opt.from,
		graphLink:    graphLink,
		architext:    opt.architext,
		architextDir: opt.architextDir,
	}
	if err := run(opt.pos[0], opt.pos[1], opt.pos[2], ro); err != nil {
		fmt.Fprintln(os.Stderr, "magma: "+err.Error())
		os.Exit(1)
	}
}

// options is the parsed command line: the flags plus the leftover positionals.
type options struct {
	force        bool
	help         bool
	version      bool
	depth        int
	hotspots     int
	from         string
	graphLink    string
	graphLinkSet bool // true once --graph-link is seen, with or without a value
	architext    bool
	architextDir string // "" => default: <repo>/docs/architext/data
	pos          []string
}

// runOpts is what run needs from the parsed command line (the flags, resolved
// down to run's own vocabulary — main resolves the --graph-link default before
// this is built, so run always sees a plain vault name or "").
type runOpts struct {
	force        bool
	depth        int
	hotspots     int
	from         string
	graphLink    string
	architext    bool
	architextDir string // "" => default: <repo>/docs/architext/data
}

// parseArgs splits the flags from the three positional args, accepting flags in
// any position. Freshness skipping is the default; --force rebuilds an
// already-fresh map. --depth/--from/--hotspots take the next argument as their
// value; --graph-link takes an optional inline value (--graph-link=VAULT).
func parseArgs(args []string) options {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--force" || a == "-f":
			o.force = true
		case a == "--help" || a == "-h":
			o.help = true
		case a == "--version" || a == "-V":
			o.version = true
		case a == "--depth":
			if i++; i < len(args) {
				o.depth, _ = strconv.Atoi(args[i])
			}
		case a == "--from":
			if i++; i < len(args) {
				o.from = args[i]
			}
		case a == "--hotspots":
			if i++; i < len(args) {
				o.hotspots, _ = strconv.Atoi(args[i])
			}
		case a == "--graph-link":
			o.graphLinkSet = true
		case strings.HasPrefix(a, "--graph-link="):
			o.graphLinkSet = true
			o.graphLink = strings.TrimPrefix(a, "--graph-link=")
		case a == "--architext":
			o.architext = true
		case strings.HasPrefix(a, "--architext="):
			o.architext = true
			o.architextDir = strings.TrimPrefix(a, "--architext=")
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
		"derives reachability views (dead code, test-only code), then writes both an Obsidian\n" +
		"markdown map and the JSON contract. Deterministic and LLM-free: same repo + SHA →\n" +
		"same graph. Skips the rebuild when a fresh map already exists.\n\n" +
		"USAGE:\n" +
		"  magma [flags] <repo-path> <folder-name> <vault-path>\n\n" +
		"ARGUMENTS:\n" +
		"  <repo-path>     path to the Git repository to analyze (language is auto-detected;\n" +
		"                  v" + version + " supports " + strings.Join(backend.Supported(), ", ") +
		" — others are refused honestly)\n" +
		"  <folder-name>   a label for this map, and the folder created for it inside the\n" +
		"                  vault. Must be a single path component (no '/', '\\', '.', '..')\n" +
		"  <vault-path>    the Obsidian vault directory the map folder is written into, as\n" +
		"                  <vault-path>/<folder-name>/ — Overview.md, per-function notes, and\n" +
		"                  hidden .magma/{graph,_dead,_test-only,manifest}.json\n\n" +
		"FLAGS:\n" +
		"  -f, --force            rebuild even when a fresh map already exists for this commit\n" +
		"      --depth N          limit per-function notes to a forward walk N levels from the\n" +
		"                         entry points (default: every function gets a note)\n" +
		"      --from SYMBOL      root the --depth walk at SYMBOL instead of the entry points\n" +
		"      --hotspots N       how many functions the Overview hotspot lists/pie show (default 10)\n" +
		"      --graph-link[=VAULT]\n" +
		"                         add a one-click Obsidian graph-view link to Overview.md\n" +
		"                         (requires the Advanced URI plugin); VAULT defaults to the\n" +
		"                         vault-path's own folder name\n" +
		"      --architext[=DIR]  also emit docs/architext/data/code-graph.json (default: <repo>/docs/architext/data)\n" +
		"  -h, --help             show this help and exit\n" +
		"  -V, --version          print the version and exit\n\n" +
		"EXAMPLE:\n" +
		"  magma ~/code/roboticus roboticus ~/maps\n" +
		"  # writes ~/maps/roboticus/{Overview.md, nodes/..., .magma/graph.json, ...}\n\n" +
		"Re-run at an audit's frozen HEAD; a 'tree' ending in '-dirty' means the working tree\n" +
		"wasn't clean, so the map can't be reproduced from its SHA.\n"
}

func run(repoArg, name, outRoot string, opts runOpts) error {
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

	var ignore []string
	if f := architextDataFile(repo, opts); f != "" {
		if rel, err := filepath.Rel(repo, f); err == nil && !strings.HasPrefix(rel, "..") {
			ignore = append(ignore, rel)
		}
	}
	meta, err := gitmeta.Load(repo, ignore...)
	if err != nil {
		return fmt.Errorf("reading git metadata (is %q a git repo?): %w", repoArg, err)
	}
	meta.Generator = "magma/" + version

	dataDir := filepath.Join(out, ".magma")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}

	// Validated is the only wall-clock read in the whole program: internal/notes
	// is pure, so "now" is captured once here and threaded through both the
	// build path and the fresh-skip Overview refresh below.
	nopts := notes.Options{
		FolderName: name,
		Hotspots:   opts.hotspots,
		Depth:      opts.depth,
		From:       opts.from,
		GraphLink:  opts.graphLink,
		CommitDate: meta.CommitDate,
		Validated:  time.Now().Format("2006-01-02 15:04 MST"),
	}

	// Building the map is the expensive step, so skip it when a current one is
	// already on disk — magma is meant to be run before every audit/code task and
	// must be near-free when nothing changed. --force overrides.
	if !opts.force && isFresh(out, meta, opts.architext) {
		return refreshOverview(out, dataDir, name, meta, nopts)
	}

	lang := detect.Detect(repo)
	b, ok := backend.For(lang)
	if !ok {
		// Still emit the (refused) artifacts and the panel, but exit non-zero so
		// the audit gate and shells see the refusal.
		g := refusedGraph(meta, lang)
		if err := emitReport(out, dataDir, repo, meta, name, g, nopts, opts); err != nil {
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
	return emitReport(out, dataDir, repo, meta, name, g, nopts, opts)
}

// isFresh reports whether a complete, current map already exists at out — the
// idempotency guard that lets magma run before every task for near-zero cost.
// Fresh means: the three JSON files are present, graph.json stamps the exact
// tree we're looking at, it was built by THIS magma version, AND every markdown
// path the manifest lists is still present on disk. A dirty tree is never fresh
// (its working-tree content can't be reproduced from the sha); a generator
// mismatch means a magma upgrade must re-map even at the same commit; a missing
// manifest or a manifest listing a note that's since been deleted means the
// vault is incomplete (a JSON-only folder from an older run, or a partially
// wiped notes set) and must be rebuilt, not silently accepted as current. A
// stored graph that was refused (Computable=false) is never fresh either — a
// refusal must always re-attempt, never be silently repeated as "already fresh".
// wantArchitext additionally requires the vault's code-graph.json mirror to be
// present — a map built before --architext was requested must rebuild to
// produce it, not be silently accepted as covering it.
func isFresh(out string, meta contract.Meta, wantArchitext bool) bool {
	if strings.HasSuffix(meta.Tree, "-dirty") {
		return false
	}
	dataDir := filepath.Join(out, ".magma")
	for _, f := range []string{"graph.json", "_dead.json", "_test-only.json"} {
		if _, err := os.Stat(filepath.Join(dataDir, f)); err != nil {
			return false
		}
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "graph.json"))
	if err != nil {
		return false
	}
	var g contract.Graph
	if err := json.Unmarshal(b, &g); err != nil {
		return false
	}
	if g.Tree != meta.Tree || g.Generator != meta.Generator {
		return false
	}
	if !g.Computable {
		return false
	}

	mb, err := os.ReadFile(filepath.Join(dataDir, "manifest.json"))
	if err != nil {
		return false
	}
	var manifest []string
	if err := json.Unmarshal(mb, &manifest); err != nil {
		return false
	}
	for _, p := range manifest {
		if _, err := os.Stat(filepath.Join(out, p)); err != nil {
			return false
		}
	}

	if wantArchitext {
		if _, err := os.Stat(filepath.Join(dataDir, architext.FileName)); err != nil {
			return false // architext requested but its artifact is missing -> rebuild
		}
	}
	return true
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
		reason = fmt.Sprintf("language %q detected but its parser is not built yet (magma %s supports %s)",
			lang, version, strings.Join(backend.Supported(), ", "))
	}
	return contract.NewGraph(meta, string(lang), "").Refuse(reason)
}

// architextDataFile returns the repo-side code-graph.json destination for this
// run (the custom --architext=DIR, or the default docs/architext/data under
// repo), or "" when --architext wasn't requested at all. Shared by emitReport
// (which writes here) and run's dirty-check ignore computation (which further
// filters out any path that resolves outside repo, since that can't affect
// the repo's dirty state).
func architextDataFile(repo string, opts runOpts) string {
	if !opts.architext {
		return ""
	}
	dir := opts.architextDir
	if dir == "" {
		dir = filepath.Join(repo, "docs", "architext", "data")
	}
	return filepath.Join(dir, architext.FileName)
}

// emitReport writes the JSON artifacts (hidden under dataDir) and the Obsidian
// markdown notes (under out), optionally dual-writes the architext code-graph,
// reconciles stale notes from the prior run, and prints the styled panel to
// stdout. It is the single point both the refused-language and the computed
// paths in run reach, so the architext emission below runs for both — an
// honest refused code-graph is required, mirroring how the other refused
// artifacts (graph.json, _dead.json, ...) are always written.
func emitReport(out, dataDir, repo string, meta contract.Meta, name string, g contract.Graph, nopts notes.Options, opts runOpts) error {
	// Finalize HERE, not inside writeArtifacts. The graph is passed by value,
	// so finalizing further down attached the per-run disclosure to that
	// function's own copy: graph.json carried it and the architext emit below —
	// built from this scope's graph — did not. A limitation declaring
	// `evidenced_by: "root_ratio"` then pointed at a field absent from the
	// document, and architext's referential-integrity check refused the whole
	// artifact. Every artifact describing one run must agree about that run,
	// which means one Finalize, above every writer.
	g = g.Finalize()

	dead, testOnly, err := writeArtifacts(dataDir, meta, g)
	if err != nil {
		return err
	}
	if archFile := architextDataFile(repo, opts); archFile != "" {
		cg := architext.Emit(g)
		if err := architext.Write(cg, archFile, filepath.Join(dataDir, architext.FileName)); err != nil {
			return fmt.Errorf("writing architext code-graph: %w", err)
		}
	}
	files, manifest := notes.Render(g, dead, testOnly, nopts)
	if err := writeNotes(out, dataDir, files, manifest); err != nil {
		return err
	}
	fmt.Print(report(newStyler(os.Stdout), name, out, g, dead, testOnly, len(files)))
	return nil
}

// writeArtifacts emits the graph and its two derived views together (so the three
// artifacts always agree on computability) into dataDir (the hidden .magma/
// subfolder) and returns the two views so the caller can report on them without
// re-deriving.
func writeArtifacts(dataDir string, meta contract.Meta, g contract.Graph) (dead, testOnly contract.Note, err error) {
	// The caller finalizes; this must not, or it would finalize its own copy and
	// leave the caller's other writers describing a different run.
	dead = g.DeadView(meta)
	testOnly = g.TestOnlyView(meta)
	if err = contract.WriteGraph(dataDir, g); err != nil {
		return dead, testOnly, err
	}
	if err = contract.Write(dataDir, "_dead", dead); err != nil {
		return dead, testOnly, err
	}
	err = contract.Write(dataDir, "_test-only", testOnly)
	return dead, testOnly, err
}

// refreshOverview handles the fresh-skip case: no re-analysis. It re-renders
// only Overview.md (the one file whose content legitimately changes between two
// fresh runs — its "Last validated" stamp) from the JSON already on disk, writes
// it, and prints the same "already fresh" notice as before this task.
func refreshOverview(out, dataDir, name string, meta contract.Meta, nopts notes.Options) error {
	b, err := os.ReadFile(filepath.Join(dataDir, "graph.json"))
	if err != nil {
		return err
	}
	var g contract.Graph
	if err := json.Unmarshal(b, &g); err != nil {
		return err
	}
	dead, err := readNoteFile(filepath.Join(dataDir, "_dead.json"))
	if err != nil {
		return err
	}
	testOnly, err := readNoteFile(filepath.Join(dataDir, "_test-only.json"))
	if err != nil {
		return err
	}

	files, _ := notes.Render(g, dead, testOnly, nopts)
	if err := writeContained(out, "Overview.md", []byte(files["Overview.md"])); err != nil {
		return err
	}

	fmt.Printf("magma: %s [%s] already fresh -> %s (use --force to rebuild)\n", name, meta.Tree, out)
	return nil
}

// readNoteFile reads and parses one of the hidden .magma/*.json Note files.
func readNoteFile(path string) (contract.Note, error) {
	var n contract.Note
	b, err := os.ReadFile(path)
	if err != nil {
		return n, err
	}
	err = json.Unmarshal(b, &n)
	return n, err
}

// writeNotes writes every rendered markdown file (creating parent directories
// as needed), deletes any note the previous run wrote that the new manifest no
// longer lists (a note magma never wrote is left untouched — see
// notes.Reconcile), and records the new manifest for the next run's
// reconciliation and freshness check. Every path is resolved strictly under out
// (see safeJoin), so neither a Render output nor a manifest read back from disk
// can write or delete outside the map's own folder.
func writeNotes(out, dataDir string, files map[string]string, manifest []string) error {
	for relPath, content := range files {
		if err := writeContained(out, relPath, []byte(content)); err != nil {
			return err
		}
	}

	var oldManifest []string
	mb, err := os.ReadFile(filepath.Join(dataDir, "manifest.json"))
	switch {
	case err == nil:
		if err := json.Unmarshal(mb, &oldManifest); err != nil {
			return fmt.Errorf("parsing prior manifest: %w", err)
		}
	case !os.IsNotExist(err):
		return err
	}

	for _, stale := range notes.Reconcile(oldManifest, manifest) {
		if err := removeContained(out, stale); err != nil {
			return err
		}
	}

	newManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	newManifest = append(newManifest, '\n')
	return os.WriteFile(filepath.Join(dataDir, "manifest.json"), newManifest, 0o644)
}

// safeJoin resolves relPath under out, refusing any path that escapes out (a
// ".." component) or passes through a symlink at any level — the containment
// guard for markdown paths, which originate from notes.Render or a manifest
// read back from disk and must never let magma write or delete outside its own
// map folder.
func safeJoin(out, relPath string) (string, error) {
	full := filepath.Join(out, relPath)
	rel, err := filepath.Rel(out, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes output root %q", relPath, out)
	}
	cur := out
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to write through symlink at %q", cur)
		}
	}
	return full, nil
}

// writeContained writes content to out/relPath (creating parent directories),
// after the safeJoin containment guard.
func writeContained(out, relPath string, content []byte) error {
	full, err := safeJoin(out, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, 0o644)
}

// removeContained deletes out/relPath, after the safeJoin containment guard. A
// stale entry that's already gone is not an error.
func removeContained(out, relPath string) error {
	full, err := safeJoin(out, relPath)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
// computable run; a refused view or graph shows its reason (never a misleading
// 0). notesCount is the number of markdown files this run wrote (len(files)
// from notes.Render) — the human-facing signal that the vault was updated.
// Fidelity is deliberately NOT shown here: it's a constant ("rta") today and
// noise to a human reader; it stays in the JSON contract for machines.
func report(s styler, name, out string, g contract.Graph, dead, testOnly contract.Note, notesCount int) string {
	lang := g.Language
	if lang == "" {
		lang = "none"
	}
	rightHdr := lang + " · " + g.Tree

	type row struct{ label, value, code string }
	var rows []row
	var footnotes []string

	if !g.Computable {
		rows = append(rows, row{"status", "refused", cRed})
		footnotes = append(footnotes, g.NotComputableReason)
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
		}
		if r := refusalReason(dead, testOnly); r != "" {
			footnotes = append(footnotes, "reachability views refused: "+r)
		}
	}
	rows = append(rows, row{"notes", humanCount(notesCount), ""})

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
	for _, n := range footnotes {
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
