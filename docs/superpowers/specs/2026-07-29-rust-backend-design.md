# Design: Rust backend — full-parity call graph via a shipped rust-analyzer helper

Date: 2026-07-29
Status: proposed
Feasibility evidence: `docs/superpowers/research/2026-07-29-rust-backend-feasibility.md`
Refs: robot-accomplice/magma#14

## Problem

magma maps Go repositories and **refuses honestly** for every other language. Rust is the next
target. Unlike Go — where `x/tools` gave magma a native, in-process, type-precise call graph
needing only the `go` toolchain — Rust offers no equivalent Go library. Achieving Go-level
fidelity therefore requires a different architecture, and the spike proved which one.

## The bar (operator decisions, locked)

1. **Full parity or don't ship.** Rust ships only when it delivers Go-equivalent capability —
   call graph, signatures, dead code, test-only — at fidelity you would act on.
2. **No variable experience.** If Rust is on the supported list, it works. No tiers, no
   asterisks, no partial support. Anything less remains an honest refusal.
3. **magma may ship its own helper binary** in another language as part of its own release.
   Decision #3 forbids *user installs*; magma's own code is fine.
4. **Executing target code is accepted** as an unavoidable characteristic of Rust.
5. **`ra_ap_*` API churn is accepted** — pin and update deliberately; every alternative is less
   secure and less maintained.

## Goals

- A Rust backend meeting the full-parity bar, validated set-identically against a compiler oracle.
- Machine-authored artifacts identical in shape to Go's (`codemap-graph/1`, `codemap-rows/1`,
  `magma-code-graph/1`) — Rust must be indistinguishable to every consumer.
- Honest refusals for every condition magma cannot satisfy, with actionable reasons.
- Interactive consent for the two side-effectful operations; honest refusal when non-interactive.

## Non-goals

- **No sandboxing in this release.** Deferred: it is substantial independent work (container or
  seatbelt confinement), and it does not block correctness — only risk posture. Sequencing:
  ship correct analysis first, confine it second, once the execution surface is known concretely.
  Tracked separately; see §Security.
- **No other languages.** One language per minor (decision #6).
- **No change to the JSON wire contracts** beyond a new `fidelity` token (§Contract).

## Architecture

Two processes, one boundary:

```
magma (Go)                                    magma-rust-helper (Rust, shipped)
  internal/backend/rust.go                      loads workspace via ra_ap_load-cargo
    ├─ detect: Cargo.toml            ──────>    enumerates workspace-local functions
    ├─ consent gates (§Gates)         JSON      walks bodies w/ macro descent
    ├─ invoke helper (subprocess)    <──────    emits {functions[], calls[]}
    ├─ build contract.Graph
    ├─ derive views
    └─ CROSS-CHECK vs cargo dead_code lint  ──> refuse on false-dead-code divergence
```

**Why a subprocess, not FFI:** the helper is a different language and a heavyweight dependency
tree; process isolation keeps magma's Go core unchanged, makes the helper independently testable,
and leaves room for later confinement (§Security) without touching the core.

### The helper: `rust-helper/` (new, in this repo)

A Rust crate built in **release mode** and shipped per target platform.

- **Release is mandatory, not a tuning choice.** Measured: 43.4s release vs 710.3s debug on a
  575k-line workspace — 16.4×. The debug build's edge phase failed to complete at all on three
  attempts. Binary: 22 MB release vs 121 MB debug.
- Dependencies pinned exactly (`ra_ap_* = "=0.0.343"` at time of writing); updates are deliberate,
  reviewed changes.
- Contract with magma: reads a workspace path + flags on argv, writes one JSON document to
  stdout, diagnostics to stderr, non-zero exit with a machine-readable reason on refusal.

### Edge extraction — `Semantics`, NOT `outgoing_calls`

**This is the spike's central correction.** `Analysis::outgoing_calls` silently omits
macro-generated call edges. Reproduced against the oracle: a `macro_rules!`-generated call
produced **zero** edges while the compiler considered the target live — i.e. **false dead code**.

Extraction walks each function body and resolves call expressions using
`ra_ap_hir::Semantics`: `descend_into_macros*` for expansion, `resolve_method_call` /
`resolve_method_call_fallback` for method dispatch, `resolve_path` for paths.

### Built and verified (2026-07-29)

The walker is no longer hypothetical. Implemented and run against the fixture, it **recovers the
macro edge** `outgoing_calls` drops:

```
outgoing_calls (7 edges)          semantics walker (8 edges)
  gen_call → generic_target         gen_call → generic_target
  live → helper                     live → helper
  main → gen_call                   main → gen_call
  main → live                       main → live
  main → speak                      main → macro_target      <-- RECOVERED
  speak → dyn_target                main → speak
  t → only_test                     speak → dyn_target
                                    t → only_test
```

Derived reachability now matches the compiler oracle exactly: dead = `{dead}`, test-only =
`{only_test}`. That is the parity condition, met on the fixture.

**Implementation requirement discovered — `attach_db` is mandatory.** rust-analyzer's type
inference requires the salsa database attached to the calling thread. `Analysis::with_db` does
this internally (`hir::attach_db_allow_change`), but `Semantics` used directly does **not** — and
inference panics deep inside `hir_ty::next_solver::interner` with *"Try to use attached db, but
not db is attached"*. All `Semantics` work must be wrapped in `ra_ap_hir::attach_db(db, || …)`.
This is not discoverable from the `Semantics` API surface; it cost a crash to find.

Walker shape: descend every body node; on `ast::MacroCall`, expand via
`Semantics::expand_macro_call` and recurse into the expansion (depth-capped against pathological
recursion); resolve `ast::CallExpr` paths via `resolve_path` → `PathResolution::Def(Function)`,
and `ast::MethodCallExpr` via `resolve_method_call`.

**Performance: measured, and the correct approach is essentially free.** On roboticus-rust
(575k lines):

| phase | `outgoing_calls` | **`Semantics` walker** |
|---|---|---|
| load | 2.2s | 1.8s |
| enumerate | 11.8s / 3,289 fns | 15.2s / **8,437** fns (`cfg(test)` enabled) |
| edges | 29.4s / 4,515 | **31.4s / 49,316** |
| **total** | 43.4s | **48.4s** |

Roughly **11× more edges for ~7% more wall time**. Correctness and performance point the same
way, so there is no trade-off to adjudicate. 48s for a 575k-line workspace is acceptable for a
pre-audit step, and freshness-skip keeps re-runs free.

Two caveats carried into implementation:

- **The walker does not deduplicate.** 49,316 is a raw count of resolved targets; Go's
  `collectEdges` aggregates per `(from, to)` pair (upgrading `dynamic`→`static` when any call site
  is static). magma must dedup identically, so the real edge count will be lower.
- Function count rose 3,289 → 8,437 purely from enabling `cfg(test)`. That is expected — Go's
  roboticus has 8,954 of 17,561 functions in test files — but it means test code roughly doubles
  the graph, and requirement 3 (excluding test-declared functions from the test-only view) is
  therefore load-bearing, not a detail.

## The five correctness facts, as Rust requirements

The Go backend's hard-won facts are recorded as prose in HANDOFF.md, so fresh extraction code does
not inherit them — the spike reproduced one of them as a live bug. They are restated here as
**testable requirements**:

| # | requirement | Rust mechanism | evidence if violated |
|---|---|---|---|
| 1 | Restrict to workspace-local code | `Crate::origin(db)` = `CrateOrigin::Local` (from `ra_ap_ide_db::base_db`) | measured: 118,081 functions unfiltered vs **3,289** filtered — 97% is deps/std |
| 2 | Separate production from test reachability | **one graph, two reachability walks** — `cfg(test)` enabled via `CargoConfig.cfg_overrides`; nodes flagged with `Function::is_test` / `is_main` / `is_bench`; magma walks reachability from all roots and from production roots separately | Go needed two package loads and got it wrong twice |
| 3 | Exclude test-declared functions from test-only | identify `#[cfg(test)]`/test-target declarations | Go had ~9,000 false rows |
| 4 | Exclude generated code from both views | build-script- and macro-generated code — which magma now *sees* because it executes build scripts | Go had 92 false rows from cgo `init` |
| 5 | Identify production roots — **and do NOT inherit Go's lib refusal** | bin `main`s **plus a library crate's `pub` API**, which rustc itself treats as roots. See §Roots below — porting Go's rule here would be a defect | Go's no-prod-main refusal exists because reachability was ~91% false positives without it; Rust's oracle answers differently |

### Requirement 2 — RESOLVED, and not the way it first appeared

`CallHierarchyConfig { exclude_tests }` looked like a free win. **It is not usable for this.**
Verified twice:

1. With `cfg(test)` off (rust-analyzer's default) no test functions exist in the graph at all, so
   the flag is inert — `exclude_tests` true and false produced identical output.
2. With `cfg(test)` enabled via `CargoConfig.cfg_overrides` (`CfgDiff::new(vec![CfgAtom::Flag(sym::test)], …)`),
   the test function and its `t → only_test` edge appear — but the two modes are **still byte-identical**.

Reading `call_hierarchy.rs` explains why: every `exclude_tests` check is on the **callee** side
(`def.is_test(db)`), filtering test functions out of *results*. It never suppresses traversal
*from* a test function that was explicitly queried. Since extraction enumerates every function,
test callers' edges come back regardless.

**The correct design mirrors Go exactly**, using primitives rust-analyzer does provide —
`Function::is_test`, `is_main`, `is_bench`:

- Enable `cfg(test)` so test code is in the graph at all.
- Emit **one** graph: every function flagged test/main/bench, every edge.
- magma computes reachability **twice** over that graph — from all roots, and from production
  (non-test) roots only — exactly as the Go backend does.

This is simpler than the flag, avoids depending on an upstream behaviour that does not match our
need, and puts graph-walking in one place. Requirement 3's mechanism (`is_test`) falls out of the
same primitive.

## Roots — a library's `pub` API is a root, and Go's refusal must NOT be ported

**Correcting a defect found reviewing this spec.** Go refuses to compute views for a scope with no
non-test `main`, because a library cannot see its external callers and reachability would be almost
entirely false positives. Porting that rule to Rust would be wrong twice over:

1. **Most Rust crates are libraries.** Blanket-refusing them would refuse most of the ecosystem —
   a direct violation of bar #2 (no variable experience).
2. **The oracle disagrees with that rule.** Verified on a lib crate with no `main`:

   ```rust
   pub fn public_api() { internal_helper(); }
   fn internal_helper() {}
   fn truly_dead() {}
   ```
   `cargo check` reports **only** `truly_dead`. `public_api` is not flagged (it is `pub`, hence a
   root) and neither is `internal_helper` (reachable from it). **rustc treats a library's public
   API as the root set.**

Since derived views must match the oracle set-identically (§Validation), magma must adopt the same
semantics:

- **Roots (production):** every `main` in a bin target, **plus every `pub` item reachable as part
  of the crate's public API** for library crates.
- **Roots (all):** the above plus test and bench functions (`is_test`, `is_bench`).
- **No lib refusal.** A library crate is analysable; it is not a degenerate scope.

**Honest semantics, stated plainly:** for a library, "dead" means *unreachable from the crate's
public API* — not *unreachable from a program entry point*. That is a weaker claim than Go's, and
it is the correct one, because a library's callers are outside the analysed scope. It is also
exactly what rustc means. This distinction belongs in the notes magma writes, not just here.

The refusal condition narrows to the genuinely degenerate case: **no roots at all** — no bin
target and no public API. Reachability is then uncomputable and magma refuses as it always has.

## Reachability views and the cross-check

Views are **derived from the graph** (preserving magma's invariant that graph and views can never
disagree), **and** validated at runtime against the compiler's own answer.

**The oracle:** rustc's `dead_code` lint under two build configurations reproduces magma's exact
split — `cargo check` gives production-unused; `cargo check --profile test` gives all-roots-unused;
dead = unused in both, test-only = the difference. Machine-readable via `--message-format=json`.
It correctly handles `dyn` dispatch, generics, and macros.

**The cross-check must classify divergences, not treat them all as fatal.** An earlier draft of
this spec said "fail loudly on any disagreement." That would make Rust support unusable, because
some divergence is *guaranteed and expected*:

`#[allow(dead_code)]` renders a genuinely-unreachable function **invisible to the lint** (verified:
zero mentions), while magma's graph correctly finds it dead. That is not a magma bug — it is the
oracle deliberately declining to answer. And it is common in real code:

| repo | `#[allow(dead_code)]` |
|---|---|
| roboticus-rust | 45 occurrences across 17 files |
| architext | 6 across 2 |
| ethrex | 3 across 3 |

`#[no_mangle]`, `#[used]`, and `#[export_name]` are the same class — live via linkage the lint
cannot see. Under a naive policy magma would refuse on roboticus-rust immediately.

### The policy

**Step 1 — normalise.** Before comparing, exclude functions carrying liveness-affecting attributes
(`allow(dead_code)`, `used`, `no_mangle`, `export_name`) from **both** sides. The oracle
deliberately says nothing about these, so they are outside the comparison's domain.

**Step 2 — classify what remains**, by direction, because the two directions have opposite
severity:

| divergence | meaning | action |
|---|---|---|
| magma says **dead**, oracle says **live** | **we missed an edge** — false dead code | **REFUSE, fail loud.** This is the failure magma exists to prevent; it is exactly the macro-edge defect (§Edge extraction) |
| magma says **live**, oracle says **dead** | we have a spurious edge — under-reporting dead code | **Record and surface** as a diagnostic. Conservative direction: it hides a finding rather than inventing one. A defect to fix, not grounds to withhold the map |

This preserves the operator's "more trustworthy path" — internal consistency *and* external
authority — while distinguishing *the oracle declined to answer* and *we are conservative* from
*we are wrong in the dangerous direction*. Its value is already demonstrated: the macro defect
would have been caught by the first row.

Attribute-suppressed exclusions must be **counted and reported**, not silently dropped — a run
that excluded 45 functions should say so, or the normalisation becomes a place for real
divergence to hide.

## Gates: consent for side effects

Two operations have side effects beyond reading a repo. Both follow one rule:
**interactive by default → prompt; `--non-interactive` → refuse honestly.**

| operation | why it is needed | flag |
|---|---|---|
| Install a pinned toolchain | 2 of 4 sampled real repos pin a `rust-toolchain.toml` version that is not installed; `cargo metadata` fails outright | `--install-toolchain` |
| Execute target code (build scripts, proc macros) | required for correctness — disabling it makes `build.rs`- and derive-generated code invisible → false dead code | `--allow-execution` |

**"Refuse" means refuse, never degrade.** A run with build scripts skipped produces false dead
code — the exact failure the full-parity bar exists to prevent. Under `--non-interactive` without
consent, magma emits a refused graph with a reason; it never emits a weaker map.

Both are new *reasons* in magma's existing refusal machinery, not new mechanism.

## Refusal reasons (all honest, all machine-readable)

- `rust-toolchain.toml requires toolchain X, which is not installed` (+ the exact rustup command)
- `analysis requires executing this repository's build scripts and proc macros; consent declined`
- `analysis requires executing …; --non-interactive supplied and --allow-execution not set`
- `no roots in scope (no binary target and no public API); reachability not computable` — the
  degenerate case only. **Not** Go's lib refusal: a library crate with a public API is analysable
  (§Roots)
- `derived views report as dead what the compiler considers live: <symbols>` (cross-check: false dead code — the fatal direction; the reverse direction is reported as a diagnostic, not a refusal)

## Contract

Rust reuses the existing wire contracts unchanged, with one addition: a new **`fidelity`** token.
Go emits `"rta"` (Rapid Type Analysis). Rust's edges come from rust-analyzer's type inference,
which is a different thing, so it gets its own token: **`"semantic"`**.

**Decided, not `"hir"`.** magma already strips fidelity jargon from its *human* output while
keeping the field in JSON for machines — but Architext renders it to users. `"hir"` is
rust-analyzer's internal vocabulary and means nothing to a reader; `"semantic"` describes what the
edges actually are. Consumers should render both tokens as human phrases rather than raw strings
(`"semantic"` → "resolved by type inference"; `"rta"` → "includes approximated dynamic calls").

**Coordination required:** the Architext side consumes `fidelity` against a frozen
`magma-code-graph/1` schema. Their validator accepts any non-empty string today, so this is
additive — but it changes what their renderer must explain. Notify before shipping, using the same
handshake as the emitter freeze.

Everything else is identical, with the Rust-to-contract mappings stated explicitly so they are not
improvised during implementation:

| contract field | Rust mapping |
|---|---|
| `pkg` | crate + module path (`mycrate::net::client`) |
| `kind` | `"method"` for anything with a `self` receiver (inherent or trait impl); `"func"` for free functions **and associated functions without a receiver** |
| `exported` | `pub` visibility — and note this is load-bearing, not cosmetic: for a library it also determines root status (§Roots) |
| `test` | `Function::is_test` |
| `root` | `Function::is_main`, plus lib `pub` API (§Roots); `is_bench` counts only toward all-roots |
| `signature` | module-relative type strings, mirroring Go's `prettyName` qualifier style |
| `generated` | build-script- and macro-generated code (requirement 4) |

**Closures are not nodes.** Go's v1 has the same limitation (calls routed through closures and
synthetic wrappers are not emitted as node edges) and it is documented as a known, labelled gap
rather than a silent one. Rust matches that treatment — consistent behaviour across languages,
per bar #2.

**Rust must be indistinguishable from Go to every consumer** — that is requirement 2 of the bar.

## Security

magma's Go analysis never executes target code. **Rust analysis does** — `build.rs` scripts and
proc macros both run, observed directly in the spike. This is inherent to Rust's compilation
model and cannot be gated away without breaking parity (accepted, decision 4).

Consequences to document loudly in the README and `--help`, not bury:

- Running magma on a Rust repository executes that repository's code.
- This is precisely the situation magma is designed for — a pre-audit step on code you have not
  yet reviewed — so the risk is real, not theoretical.
- Sandboxing is the mitigation and is **deferred** (see Non-goals). Until it lands, the consent
  gate (§Gates) is the control: the user is told what will happen and must agree.

## Validation — the parity proof

Parity is a claim until proven. Go's was proven set-identical against `deadcode` at a pinned SHA,
both directions. Rust's bar is the same:

1. **Fixture tests** mirroring Go's `livemod`/`libmod`, plus Rust's hard cases: `dyn` dispatch,
   generics, `macro_rules!`, proc-macro derives, build-script-generated code, workspaces.
2. **Oracle diff on real workspaces** — derived views vs the `dead_code` lint, set-identical in
   both directions, on at least: a single-crate repo, a workspace, and one large real repo
   (roboticus-rust, 575k lines, 8,437 workspace-local functions with `cfg(test)` enabled).
3. **Cross-check green** on all of the above — no divergence.
4. **Coverage** ≥ 80% on the Go side; the helper carries its own Rust tests.

Until (2) passes set-identically, Rust does not ship. That is what decision 1 means.

## `/clean-architecture` — boundaries

- **The helper is an outer-ring detail.** `internal/backend/rust.go` implements the existing
  `Backend` interface; nothing in `contract` or the rest of magma learns that a subprocess or
  rust-analyzer exists. Swapping the helper's internals touches one file.
- **The process boundary carries a DTO**, not a leaked model: the helper emits plain JSON, which
  `rust.go` maps into `contract.Graph`. rust-analyzer types never enter magma's core.
- **The cross-check is a policy, not a mechanism** — it consumes the derived views and the oracle
  output and decides; it does not reach into extraction.
- **Proportional:** one new Go file, one new Rust crate. No framework.

## `/clean-code` — quality gate

- Small units: workspace loading, enumeration, body-walking, and serialization are separate in the
  helper; consent, invocation, mapping, and cross-check are separate on the Go side.
- Names by domain (`workspaceLocalFunctions`, `resolveCallTargets`, `crossCheckAgainstLint`), not
  by mechanism.
- No magic literals: the pinned `ra_ap` version, helper binary name, and refusal reasons are named
  constants in one place each.
- Tests encode *why* — e.g. "a macro-generated call must produce an edge, because without it the
  target is falsely reported dead" — citing the reproduced defect.
- Pre-merge: re-invoke `/clean-code` and `/clean-architecture` on the diff.

## Sequencing

1. Helper skeleton: load workspace, enumerate workspace-local functions (requirement 1), emit JSON.
2. `cfg(test)` enablement + the prod/test split — **resolved during the spike** (`exclude_tests`
   falsified; use `is_test`/`is_main`/`is_bench` + two reachability walks). Port the proven
   approach rather than re-deriving it.
3. `Semantics` body-walking with macro descent; re-measure performance.
4. Go-side backend: invoke, map to `contract.Graph`, derive views.
5. Oracle cross-check + fail-loud.
6. Consent gates and refusal reasons.
7. Parity validation (§Validation) — the gate on shipping.
8. Release pipeline: cross-compile the helper per platform.

Steps 2 and 3 were the design's two real risks. **Both were retired during the spike** — the
prod/test split is settled and the Semantics walker is built, verified against the oracle, and
performance-measured. What remains is porting proven approaches, not discovering them.
