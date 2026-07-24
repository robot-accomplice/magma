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
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: magma <repo-path> <name> <output-root>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "magma: "+err.Error())
		os.Exit(1)
	}
}

func run(repoArg, name, outRoot string) error {
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

	lang := detect.Detect(repo)
	b, ok := backend.For(lang)
	if !ok {
		return writeRefusal(out, meta, lang)
	}

	g, err := b.BuildGraph(repo, meta)
	if err != nil {
		return err
	}
	return writeAll(out, meta, g, name)
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
	if err := writeAll(out, meta, g, ""); err != nil {
		return err
	}
	return fmt.Errorf("%s", reason)
}

// writeAll emits the graph and its two derived views together, so the three
// artifacts always agree on computability.
func writeAll(out string, meta contract.Meta, g contract.Graph, name string) error {
	if err := contract.WriteGraph(out, g); err != nil {
		return err
	}
	if err := contract.Write(out, "_dead", g.DeadView(meta)); err != nil {
		return err
	}
	if err := contract.Write(out, "_test-only", g.TestOnlyView(meta)); err != nil {
		return err
	}
	if name != "" {
		fmt.Printf("magma: %s [%s @ %s] %d nodes, %d edges -> %s\n",
			name, g.Language, g.Tree, len(g.Nodes), len(g.Edges), out)
	}
	return nil
}
