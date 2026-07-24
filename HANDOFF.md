# magma — handoff

**What it is:** a standalone Go CLI at `~/code/magma` that extracts a repository's
**call graph** (functions/methods = nodes, calls = edges) and derives reachability
views (dead code, test-only code) from it. Deterministic and **LLM-free** — same repo
+ SHA → same graph every time — so it runs as a **tokenless prerequisite to any code
audit**, in CI, or as a git hook. Module path: `github.com/robot-accomplice/magma`.

It replaces the old Python `codemap` skill (and an abandoned gosh rewrite — see below).

## Locked design decisions (from the operator, this session)

1. **Standalone app, own repo** (`~/code/magma`), not a skill and not gosh. The skill
   will shrink to a thin wrapper that invokes the `magma` binary.
2. **The artifact is a call graph.** `graph.json` (contract `codemap-graph/1`) is the
   primary output; `_dead.json` / `_test-only.json` (contract `codemap-rows/1`) are
   **derived views** over it, kept so the existing slop-audit gate keeps working.
3. **No third-party analyzer binaries. No npm installs.** Only the language's own
   toolchain may be assumed present. A *library compiled into magma* is fine (it's
   magma's own code); a binary the user must install (`deadcode`, `knip`, `detekt`,
   `pmd`, `jscpd`) is NOT. This is why the Go backend uses `x/tools` in-process
   instead of shelling to `deadcode`.
4. **Implemented in Go**, deliberately: the flagship target is Go (roboticus, the CLIs),
   and Go's type-precise call graph (`go/packages`+`ssa`+`callgraph/rta`) is a native Go
   library, in-process, needing only `go`. Rust was considered and rejected: it would
   force shelling out for the Go backend, violating decision #3 for the primary target.
5. **"We build the parsers, one at a time."** Magma owns each language's analysis. Go is
   first (x/tools). Other languages land one per release, each leaning on a heavy-lifting
   parse library — **tree-sitter** (C lib, grammar per language, Go bindings, compiled in;
   not a user install) — with the graph walk built on top. Until a language's parser
   exists, its backend **refuses honestly**, never fakes.
6. **Releases are gated by language support.** `v0.1.0 = complete Go` (no external deps,
   full call graph). Each subsequent minor adds one language.
7. **80% LOC unit-test coverage** is part of the v0.1.0 bar. **DONE — 85.4% overall.**
8. **It goes into service the moment v0.1.0 clears the bar** (call graph + 80% coverage) —
   as the mandatory pre-audit step, starting with roboticus.

## v0.1.0 STATUS — DELIVERED & TESTABLE (session 2026-07-24)

**magma is an independent project. No coupling to Claude/skills/gate/vault** — it is a Go binary
that maps a repo, nothing more. Anything below about codemap/slop-ferret lives OUTSIDE this repo
(`~/.claude/skills/…`) and is post-release integration, not part of magma.

Delivered this session, on top of the validated backend:
- **Unit tests — 85.4% overall** (bar is 80%). `go test ./...`, `go vet`, `gofmt` all clean.
  Only `main()`'s os.Exit wrapper is 0% (untestable without a subprocess). No analysis code changed.
- **Self-refresh / idempotency** (new, TDD'd): magma skips the expensive rebuild when a **fresh** map
  already exists at `<out>/<name>/` — all three files present, stamped with THIS magma version and the
  exact **clean** tree. A dirty tree or a version bump forces a rebuild. `--force` overrides. This makes
  magma near-free to run before every task. See `isFresh`/`parseArgs` in `main.go`.
- **Installed**: `go install .` → `~/go/bin/magma` (on PATH).

Re-validated against the real `deadcode` oracle through the INSTALLED binary on roboticus at HEAD
`15d7b47c` (working tree), by `file:line` set comparison both directions:

| view      | deadcode | magma | |
|-----------|----------|-------|-|
| `_dead`   | 105      | 105   | **set-identical, 0 diff either direction** |
| `_test-only` | 623   | 623   | **set-identical, 0 diff either direction** |

graph.json on roboticus: **17,343 nodes, 48,988 edges**, `fidelity: "rta"`, computable.
(Original validation was 17,307/48,884 at ebcef101 — the small delta is the different HEAD.)

## Original state — what was DONE and VERIFIED (session 1)

Go backend is **built and validated**. `go build ./...` and `go vet ./...` are clean.

Validated against the real `deadcode` on roboticus at the *same HEAD* (ebcef101): `_dead` 105==105,
`_test-only` 623==623 (set-identical). graph.json: 17,307 nodes, 48,884 edges.

Run it:
```sh
cd ~/code/magma && go build -o /tmp/magma .
/tmp/magma <repo-path> <name> <output-root>
# e.g. /tmp/magma /Users/jmachen/code/roboticus roboticus /tmp/mg
# writes /tmp/mg/roboticus/{graph.json,_dead.json,_test-only.json}
```

Oracle for regression (must stay exact at a pinned HEAD):
```sh
cd /Users/jmachen/code/roboticus && export GOTOOLCHAIN=local GOFLAGS=-mod=readonly
MOD=$(go list -m)
deadcode -test -json ./... | jq --arg m "$MOD" '[.[]|select(.Path|startswith($m))|.Funcs[]]|length'  # RAW_ALL = dead
deadcode      -json ./... | jq --arg m "$MOD" '[.[]|select(.Path|startswith($m))|.Funcs[]]|length'  # RAW_PROD
# deadcode test-only = RAW_PROD - RAW_ALL ; run oracle + magma in ONE script (branch is live)
```

## File map

```
main.go                        CLI: arg parse, output containment, dispatch, writeRefusal/writeAll
internal/contract/contract.go  Meta, Row, Note, Computed/Refused, Write (codemap-rows/1)
internal/contract/graph.go     Node, Edge, Graph, NewGraph/Refuse, DeadView/TestOnlyView, WriteGraph
internal/detect/detect.go      manifest-based language detection (go.mod/Cargo.toml/package.json/gradle/pom)
internal/backend/backend.go    Backend interface + registry (For/register)
internal/backend/golang.go     the Go RTA backend (the meat)
internal/gitmeta/gitmeta.go    git sha + dirty → contract.Meta
```

## Hard-won correctness facts (do NOT relearn these)

The derived views were wrong three times before matching deadcode. The fixes, all in
`golang.go` / `graph.go`:

1. **Restrict nodes to the module** (`inModule`/`moduledPath`). Without it the graph
   includes the entire dependency closure (47k nodes, 7.7k false "dead"). deadcode
   applies the same `^<module>\b` filter to its output.
2. **Two package loads, not one.** `Tests:true` for the emitted graph + all-roots
   `Reachable`; a SEPARATE `Tests:false` load for `ProdReachable`. A single Tests:true
   load creates duplicate SSA variants (`p` and `p [p.test]`) and production reachability
   over that mixed program is garbage. This is exactly why deadcode runs two processes.
   Positions (file:line:col) are identical across loads, so the two reachability sets
   compose by position.
3. **`_test-only` must exclude test-file nodes** (`!n.Test`). Functions *declared in*
   `_test.go` are `Reachable` (from test mains) but never `ProdReachable` (absent from the
   prod load) — counting them added ~9,000 false test-only rows. deadcode's prod load
   never sees test files, so it never had this problem.
4. **`_test-only` must also exclude generated nodes** (`!n.Generated`). cgo emits
   `func init()` in build-cache files; these are reachable-with-tests, not prod-reachable,
   not in `_test.go` → 92 false test-only rows (all `init#1` with go-build-cache paths).
   `DeadView` already excluded generated; `TestOnlyView` now matches.
5. **No-production-main refusal** (`hasProdRoot`, gates both views). A scope with no
   non-test `main` can't see its external callers, so reachability would be ~91% false
   positives (the concrete failure that killed the old approach). `graph.json` is still
   emitted (test-rooted), but the reachability VIEWS refuse with a reason. `Root` is set
   only on real `func main` in a main package.

Other facts:
- `fidelity: "rta"` — edges are Rapid Type Analysis. Static calls exact; dynamic
  (interface/func-value) are the RTA over-approximation. **v1 edge limitation, KNOWN and
  labeled:** calls routed through closures or synthetic wrappers are not yet emitted as
  node edges (only direct source-declaration → source-declaration edges). Improve later;
  it's honest, not faked.
- `prettyName` is a self-contained fork of deadcode's (can't import `x/tools/internal`);
  it renders `(*pkg.T).F` → `T.F` via `types.Named` on the receiver.
- Wall-2 hardening: the Go loads set `GOTOOLCHAIN=local` + `GOFLAGS=-mod=readonly`
  (no network/toolchain fetch, no go.mod mutation). Go analysis never *executes* target
  code (type-check + IR only), so the RCE surface is low — but tree-sitter/other-lang
  backends that run builds will need the sandbox+allowlist thinking.
- Non-Go / unknown languages: `writeRefusal` emits a refused graph + refused views with a
  reason, and exits non-zero — the audit gate sees an explicit refusal, never a missing
  file.

## Remaining work for v0.1.0 (the bar to clear before it goes into service)

1. **Unit tests to 80% LOC coverage** — **DONE (85.4% overall, `go test ./...` green,
   gofmt/vet clean).** No production code changed; only `_test.go` files + `internal/backend/testdata/`
   fixtures (`livemod` = main→Live→T.M with Dead/*T.P/OnlyTest; `libmod` = no-prod-main → views refuse).
   `main()`'s os.Exit wrapper is the only 0%-covered function (untestable without a subprocess). Original plan:
   - `contract`: pure tests for `Computed`/`Refused`/`DeadView`/`TestOnlyView` (all
     filter combinations: dead, test-only, generated-excluded, test-excluded,
     no-prod-root refusal, not-computable passthrough), `Write`/`WriteGraph`.
   - `detect`: temp dirs per manifest + ordering (kotlin before java) + unknown.
   - `gitmeta`: temp `git init` repo, commit, assert clean sha then dirty suffix.
   - `backend/golang`: a `testdata/` fixture module (module `magmafixture`, a `main.go`
     calling `Live()`, a `Dead()` never called, an `OnlyTest()` called only from a
     `_test.go`) → `BuildGraph` → assert nodes/edges + exactly-Dead=`Dead`,
     exactly-TestOnly=`OnlyTest`. This is the file that needs coverage most (largest).
     Also a no-main fixture → assert views refuse with `noProdMain`.
   - `main`: `prepareOutput` (name validation, traversal/containment rejection) +
     `writeRefusal` on an unknown-language temp dir.
   Coverage check: `go test ./... -coverprofile=cover.out && go tool cover -func=cover.out | tail -1`
## POST-RELEASE integration (deferred — operator-directed, NOT part of magma)

magma is an independent project; the items below are Claude-side wiring the operator explicitly
scoped as **post-release, to be done once magma is proven** ("we'll create a skill to invoke it").
They do NOT belong in the magma repo. Deferral is a scope decision by the operator, not dropped work.

2. **Thin codemap skill wrapper** at `~/.claude/skills/codemap/`. Status this session: a draft thin
   `SKILL.md` was written (binary usage, graph+views, refresh/staleness, Go-only refusal), and the
   abandoned gosh/python/rust artifacts were **archived** (not hard-deleted — `~/.claude` isn't
   git-backed) to `versions/abandoned-2026-07-24-gosh-python/`. The operator flagged the SKILL.md
   itself as a post-release concern, so it's a draft, not final.
3. **Wire magma as a HARD prerequisite** into slop-ferret / code audit. **Blocker found & specced:**
   `slop-ferret/scripts/gate.py` currently hard-requires FOUR files
   (`_dead`,`_test-only`,`_duplicates`,`_interfaces`) and only knows `fidelity:"reachability"`. magma
   emits `graph.json` + `_dead.json` + `_test-only.json` with `fidelity:"rta"` — it does NOT (and
   per decision #5 must not FAKE) `_duplicates`/`_interfaces`. Honest fix (update the gate, not magma):
   - `MAP_FILES` → require only `["_dead.json","_test-only.json"]`; make `_duplicates`/`_interfaces`
     **optional** (load+use if present, skip if absent) so future backends can still feed families D/E.
   - Add `"rta": ""` to `FIDELITY_BAR` (RTA is a real call graph → strong bar, like `reachability`).
   - Update `tests/test_gate.py` `write_map` (drop the two files / add an rta-fidelity + 2-file case).
   The gate's sha-pinning already matches magma's short `sha`, and `codemap-rows/1` is unchanged.

## Future releases (one language per minor, decision #5/#6)

Each: detect already returns the language (`detect.go` maps Cargo.toml→rust,
package.json→node, gradle→kotlin, pom.xml→java). Add a backend implementing the
`Backend` interface, register it in `init()`, using tree-sitter for the parse and building
the call-graph walk on top. Node/TS especially: use the project's own `tsc`/`node` if a
semantic graph is wanted, but **no npm-installed analyzers**. Update the version to reflect
the added language.

## Task list (harness tasks #5–#8)

- #5 skeleton + contract — DONE
- #6 Go call-graph backend — DONE + verified exact
- #7 other-language backends — future releases (honest-refuse is the v0.1.0 behavior)
- #8 skill wrapper + refresh + audit prerequisite — TODO (now the immediate next task; tests are DONE)

## Abandoned path (don't revisit)

A gosh rewrite of codemap consumed most of the prior session. **gosh is too broken to
script this** (no loops, broken `test`/redirect/`$()`, jq-in-`$()` mangling). It's fully
abandoned in favor of this Go binary. If any `*.gosh` files remain under the codemap
skill, delete them.
