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

The only runtime requirement is a working `go` toolchain (magma analyzes Go by building a
type-precise call graph in-process — no third-party analyzer binaries to install).

## Usage

```
magma [--force] <repo-path> <folder-name> <vault-path>
```

| argument | meaning |
|---|---|
| `<repo-path>` | the Git repository to analyze (language auto-detected) |
| `<folder-name>` | a label for this map, and the folder created for it inside the vault (a single path component) |
| `<vault-path>` | the vault directory the map folder is written into, as `<vault-path>/<folder-name>/` |

| flag | meaning |
|---|---|
| `-f, --force` | rebuild even when a fresh map already exists for this commit |
| `-h, --help` | show the full help screen |
| `-V, --version` | print the version |

Run `magma --help` for the complete screen.

### Output

A run writes three files to `<vault-path>/<folder-name>/`:

| file | contract | what it is |
|---|---|---|
| `graph.json` | `codemap-graph/1` | **the primary artifact** — every function/method (node) and call (edge) |
| `_dead.json` | `codemap-rows/1` | derived view: source reachable from no root (production-dead) |
| `_test-only.json` | `codemap-rows/1` | derived view: production code reached **only** through tests |

The two views are **derived from the graph**, so they can never disagree with it about
computability. Every row is a **candidate**, not a verdict: reflection, `encoding/json`
interfaces, cgo, `go:linkname`, and entry points invoked from outside the repo all produce
callers static analysis cannot see. A candidate that survives those rules is worth reading.

`fidelity: "rta"` names what an edge *means*: static calls are exact; dynamic
(interface / function-value) calls are the Rapid Type Analysis over-approximation. Calls
routed through closures or synthetic wrappers are not yet emitted as node edges — a known,
labeled v1 limitation, not a silent gap.

### Self-refresh

Building the graph is the expensive step, so magma **skips it when a current map already
exists** — the three files are present, were built by this magma version, and stamp the exact
**clean** tree you're on. It rebuilds when the map is missing, the commit moved, magma was
upgraded, or the working tree is dirty. That makes it safe and near-free to run before every
task. Force a rebuild with `--force`.

A `tree` ending in `-dirty` means the working tree had uncommitted changes when the map was
built, so it can't be reproduced from its SHA. For an audit, run magma at the frozen HEAD and
pin downstream artifacts to the `sha` it stamps.

### Refusals

magma exits non-zero and writes a refused (but present) set of files when it cannot stand
behind a map:

- **Non-Go / unknown language.** v0.1.0 supports Go only; other languages are detected and
  refused. Support lands one language per minor release.
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
target (magma is pure Go), and publishes a checksummed GitHub Release.

```bash
git tag v0.1.0 && git push origin v0.1.0
```

## License

[MIT](LICENSE) © Robot Accomplice
