# magma

[![CI](https://github.com/robot-accomplice/magma/actions/workflows/ci.yml/badge.svg)](https://github.com/robot-accomplice/magma/actions/workflows/ci.yml)
[![Release](https://github.com/robot-accomplice/magma/actions/workflows/release.yml/badge.svg)](https://github.com/robot-accomplice/magma/actions/workflows/release.yml)
[![coverage ≥80%](https://img.shields.io/badge/coverage-%E2%89%A580%25%20enforced-brightgreen)](.github/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/robot-accomplice/magma)](https://goreportcard.com/report/github.com/robot-accomplice/magma)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**A deterministic, LLM-free call-graph and reachability mapper.**

magma extracts a repository's **call graph** — every function and method is a node, every
call is an edge — and derives **reachability views** from it: production-dead code and
test-only code. Given the same repository at the same commit it produces the same graph
every time, so it runs as a **tokenless prerequisite to any code audit**, in CI, or as a
git hook. Walking an existing map is far cheaper than asking a model to re-derive
relationships from source.

```console
$ magma ~/code/roboticus roboticus ~/maps
magma: roboticus [go @ 15d7b47c] 17343 nodes, 48988 edges -> ~/maps/roboticus
```

## Why

Most defects that matter are **relational** — "what still reaches this?", "is this shipped
code kept alive only by a test?", "what is unreachable from every entry point?" — and
reading finds them expensively or not at all. magma derives those relations once, precisely,
and writes them as plain JSON you can query.

It is deliberately **honest over convenient**: where a call graph cannot be computed (an
unsupported language, a scope with no production entry point, code that does not type-check)
magma **refuses with a machine-readable reason** instead of emitting a map that reads as
authoritative but isn't.

## Install

```bash
go install github.com/robot-accomplice/magma@latest
```

Or from a checkout:

```bash
git clone https://github.com/robot-accomplice/magma && cd magma
just install      # or: go install .
```

Analyzing **Go** needs only a working `go` toolchain — magma builds a type-precise call graph
in-process, with no third-party analyzer binaries to install.

Analyzing **Rust** additionally needs the `magma-rust-helper` binary, which links rust-analyzer as
a library and so cannot ship through `go install`:

```bash
cargo install --path rust-helper   # from a magma checkout
```

The helper is **not** part of the published GitHub Release — that archive carries the pure-Go
`magma` binary only — and it is not on crates.io. Analyzing Rust therefore needs a checkout and a
Rust toolchain even if you installed `magma` itself from a release archive or `go install`.

magma finds it on `PATH`, or at `$MAGMA_RUST_HELPER`. Without it, a Rust repo is **refused** with
an install hint rather than analyzed partially — magma returns an honest map or an honest refusal,
never a degraded one.

**Rust analysis is slow, and you should expect that.** It loads the workspace twice through
rust-analyzer (once with `cfg(test)` off, once on) and type-checks every file, because a workspace
that does not type-check gets a refusal rather than a half-map. Measured on a 1,388-file,
10,921-function workspace: **~20 minutes**, single invocation, most of it type-checking. Small
crates are seconds. Progress is reported live throughout, so a long run is distinguishable from a
hung one. Go analysis is unaffected and remains in-process and fast.

## Usage

```
magma [flags] <repo-path> <folder-name> <vault-path>
```

| argument | meaning |
|---|---|
| `<repo-path>` | the Git repository to analyze (language auto-detected) |
| `<folder-name>` | a label for this map, and the folder created for it inside the vault (a single path component) |
| `<vault-path>` | the Obsidian vault directory the map folder is written into, as `<vault-path>/<folder-name>/` |

| flag | meaning |
|---|---|
| `-f, --force` | rebuild even when a fresh map already exists for this commit |
| `--depth N` | limit per-function notes to a forward walk N levels from the entry points (default: every function gets a note) |
| `--from SYMBOL` | root the `--depth` walk at `SYMBOL` instead of the entry points |
| `--hotspots N` | how many functions the Overview hotspot lists/pie show (default 10) |
| `--graph-link[=VAULT]` | add a one-click Obsidian graph-view link to `Overview.md` (requires the Advanced URI plugin); `VAULT` defaults to the vault-path's own folder name |
| `-h, --help` | show the full help screen |
| `-V, --version` | print the version |

Run `magma --help` for the complete screen.

### Output

A run writes an Obsidian markdown map to `<vault-path>/<folder-name>/`: `Overview.md` (a
dashboard: size, reachability, hotspots, entry points), `Dead code.md` / `Test-only code.md` /
`Packages.md` (index notes), and one note per function under `nodes/`. The JSON contract is
written alongside it, hidden under `<vault-path>/<folder-name>/.magma/`:

| file | contract | what it is |
|---|---|---|
| `.magma/graph.json` | `codemap-graph/1` | **the primary artifact** — every function/method (node) and call (edge) |
| `.magma/_dead.json` | `codemap-rows/1` | derived view: source reachable from no root (production-dead) |
| `.magma/_test-only.json` | `codemap-rows/1` | derived view: production code reached **only** through tests |
| `.magma/manifest.json` | — | the sorted list of markdown paths this run wrote, used to reconcile stale notes on the next run and to gate freshness |

The two views are **derived from the graph**, so they can never disagree with it about
computability. Every row is a **candidate**, not a verdict: reflection, `encoding/json`
interfaces, cgo, `go:linkname`, and entry points invoked from outside the repo all produce
callers static analysis cannot see. A candidate that survives those rules is worth reading.

### `fidelity` — what an edge means

Every artifact carries `fidelity`, naming what an edge means for the backend that produced it.
This is the published vocabulary:

| value | backend | meaning |
|---|---|---|
| `rta` | Go | Static calls exact; dynamic (interface / function-value) calls are the Rapid Type Analysis over-approximation. Calls routed through closures or synthetic wrappers are not yet emitted as node edges — a known, labeled limitation, not a silent gap. |
| `semantic` | Rust | Edges come from rust-analyzer's name resolution and type inference, so a resolved call lands on the impl rustc would select. Desugared forms (operators, `for`, `await`, format args, `?`, `Drop`) resolve from types rather than syntax and are emitted `dynamic` — real over-approximations, never invented edges. |

**Both are real call graphs.** Both over-approximate dynamic dispatch and never invent an edge, so
every imprecision fails toward "live": a node that either map calls dead is dead conservatively.

**The value is an OPEN set, not a closed enum.** magma adds a language per minor release and each
may name its own fidelity. A consumer that branches on this field must therefore handle an
unrecognised value explicitly — **do not silently fall back to a weakest-case bar**, which is
wrong in the safe direction and therefore invisible. A downstream gate did exactly that and spent
a full sweep treating a genuine call graph as "a guess with no call graph". Announce the unknown
value instead.

(The field lives in the JSON for tooling; the terminal panel and the notes don't repeat it in
jargon a human has to look up.)

### `limitations` — what a backend cannot do

Every artifact declares its backend's known limitations upfront, ahead of any row, and every
refusal names **who** cannot do the thing. The attribution is the useful part: it tells you
whether waiting helps.

| scope | meaning | moves? |
|---|---|---|
| `language` | inherent to the analysed language | never |
| `analyzer` | the pinned analyser's limit | when the pin moves |
| `backend` | magma has not built it yet | we can fix it |

`effect` names which way a limitation errs: **`over-approximates-live`** (findings suppressed —
the map may report *fewer* dead functions than exist), `may-omit-edges`, `may-omit-nodes`.

**Both are OPEN sets, not closed enums**, for the same reason as `fidelity`, and the lesson was
learned expensively: a pinned enum elsewhere in this contract made a 15 MB artifact wholly
invalid the moment one new value appeared. A consumer must treat an unrecognised `scope` or
`effect` as one unknown annotation — never as grounds to reject the artifact.

Alongside it, `disclosure` reports what **this run** measured: `nodes`, `roots`, `generated`,
`dynamic_edges`, and `root_ratio`.

**`root_ratio` is the one to watch.** A high ratio means most of the graph is an entry point, so
few functions *can* be reported dead — and a small `_dead` set is then absence of evidence
rather than evidence of absence. Measured on a real derive-heavy Rust crate: 136 of 156 nodes
rooted (0.872) and **zero** dead functions reported. Every number was accurate, and a consumer
computing `dead = !reachable && !root` rendered it as a clean bill of health. That is the
failure this field exists to make visible.

Regenerating a map **reconciles** the notes: any markdown file the previous run wrote that
the new render no longer lists (a function that was deleted, say) is removed. A note you
wrote by hand, that magma never generated, is never touched.

### Viewing one project's graph in Obsidian

A vault usually holds several maps (and your own notes) in one graph view — so the global
graph is a hairball of everything at once. Every note magma writes carries a per-project tag,
`#magma/project/<folder-name>`, so you can isolate a single project:

- **Its call graph.** Open the graph view (⌘/Ctrl-G) and type `tag:#magma/project/<folder-name>`
  into the graph filter (or `path:"<folder-name>/nodes"`). Only that project's function notes and
  their call edges remain.
- **Its note list.** Click the `#magma/project/<folder-name>` tag anywhere (it's rendered live at
  the top of `Overview.md`) to open search scoped to that project.

Every `Overview.md` opens with a `> [!tip]` callout spelling this out, so the filter is one glance
away. (magma never edits your vault's Obsidian settings — it only writes its own map folder; the
tag is the non-invasive way to scope the graph.)

### Self-refresh

Building the graph is the expensive step, so magma **skips it when a current map already
exists** — the JSON is present, was built by this magma version, stamps the exact **clean**
tree you're on, and every markdown note the last run wrote is still on disk. It rebuilds when
the map (or any part of the note set) is missing, the commit moved, magma was upgraded, or the
working tree is dirty. On a skip, only `Overview.md`'s "Last validated" stamp is refreshed — no
re-analysis. That makes it safe and near-free to run before every task. Force a rebuild with
`--force`.

A `tree` ending in `-dirty` means the working tree had uncommitted changes when the map was
built, so it can't be reproduced from its SHA. For an audit, run magma at the frozen HEAD and
pin downstream artifacts to the `sha` it stamps.

### Refusals

magma exits non-zero and writes a refused (but present) set of files when it cannot stand
behind a map:

- **Unsupported / unknown language.** Go and Rust are supported; other languages are detected and
  refused. Support lands one language per minor release — **v0.4.0 is the JavaScript family
  (TypeScript, Node, Next, React)**. v0.3.0 is a contract release and adds no language: each
  minor is one substantial change, and language support lands one per minor.
- **Rust helper not installed.** A Rust repo is refused, with the install command, when
  `magma-rust-helper` is on neither `PATH` nor `$MAGMA_RUST_HELPER`.
- **No production `main` in scope.** A library or a single-package scope has no external-caller
  root, so reachability would be almost all false positives. `graph.json` is still emitted
  (test-rooted); `_dead` and `_test-only` refuse.
- **Type errors.** If the target does not type-check, magma refuses rather than emit a partial
  graph as a whole one.

## Development

Requires Go (see [go.mod](go.mod)), plus [`just`](https://github.com/casey/just) and
[`golangci-lint`](https://golangci-lint.run/) for the full workflow.

```bash
just            # list all recipes
just ci         # full local validation: gofmt + vet + lint + build + test + 80% coverage gate
just test       # tests with the race detector
just cover      # coverage report + gate
just run ~/code/some-repo some-repo /tmp/maps
```

`just ci` mirrors [`.github/workflows/ci.yml`](.github/workflows/ci.yml) exactly, so a green
`just ci` locally means a green CI.

### Correctness

magma's reachability views are validated against the reference
[`deadcode`](https://pkg.go.dev/golang.org/x/tools/cmd/deadcode) analyzer: on roboticus
(~17k nodes) the `_dead` and `_test-only` sets are **identical to `deadcode`'s**, by
`file:line`, in both directions. The unit suite additionally exercises every filter branch
against small fixture modules, with an enforced **80% coverage floor**.

## Releasing

Branching follows a modified gitflow: `feature/* → develop`, and `develop → main` at release
time. Pushing a semver tag drives [`.github/workflows/release.yml`](.github/workflows/release.yml),
which re-runs the CI gates, verifies the tag matches the binary's version, cross-compiles every
target (the `magma` binary is pure Go), and publishes a checksummed GitHub Release.

Tags are **annotated** — the release is cut from `main` after the promotion PR merges:

```bash
git tag -a vX.Y.Z -m "magma vX.Y.Z — <one-line summary>" && git push origin vX.Y.Z
```

### Release checklist

Run through this **before** pushing the tag; a tag is public the moment it lands.

- **Audit the user-facing docs against what the release actually does** — this README and
  [`rust-helper/README.md`](rust-helper/README.md). Supported languages, the refusal list, the flag
  table, the roadmap line, and the example output all drift silently, and **nothing in CI catches a
  README that lies**. This is the step most likely to be skipped and the only one a user sees.
- **Confirm `version` in `main.go` matches the tag.** The release workflow hard-fails on a mismatch,
  and it fails *in public*, after the tag exists.
- **Remember `release.yml` runs the Go gates only.** The Rust oracle gate lives in `ci.yml`, so the
  green `develop → main` PR is the last point Rust correctness is actually checked — not the tag.
- **Check the release artifacts after publishing**, not just the workflow's exit status: the run can
  succeed while the Release is a draft or missing a target.

## License

[MIT](LICENSE) © Robot Accomplice
