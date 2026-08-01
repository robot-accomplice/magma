# Rust backend — feasibility spike findings

Date: 2026-07-25 (spike run) / 2026-07-29 (recovered to repo)
Status: spike complete — **GO**, with one architectural correction
Feeds: the Rust backend spec (not yet written)

> **Provenance note.** The spike ran in an ephemeral scratchpad that was wiped at a session
> boundary; the helper, fixture, and original findings file were lost. This document is
> reconstructed from the session record. Every claim below was *observed* during the spike —
> nothing here is inferred or remembered second-hand — but the artifacts no longer exist to
> re-verify against. Throughput and binary size were re-measured on 2026-07-29 with a rebuilt helper (§7).
>
> **Lesson recorded:** spike deliverables belong in the repo, not the scratchpad.

## Context

magma's bar for a new language (operator decisions, 2026-07-25):

1. **Full parity or don't ship.** A language ships only when it delivers Go-equivalent
   capability — call graph, signatures, dead code, test-only — at fidelity you would act on.
   No tiers, no partial support, no asterisks.
2. **No variable experience across supported languages.** If a language is on the list, it
   works; anything less stays an honest refusal.
3. **magma may ship its own helper binary** written in another language as part of its own
   release (decision #3 permits magma's own code; it forbids *user installs*).

## Verdict: GO, with one architectural correction

A working helper was built and run. It loads a Cargo workspace through rust-analyzer driven as
a library, enumerates functions (module-level and impl methods), and extracts call edges —
including `dyn` dispatch.

**The correction:** the convenient IDE API `Analysis::outgoing_calls` is **not sufficient**. It
silently omits macro-generated call edges, producing false dead code. Edge extraction must be
built on `hir::Semantics` body-walking with explicit macro descent. See §3.

## 1. The oracle is solved — and it mirrors magma's Go architecture

rustc's own `dead_code` lint, run under two build configurations, yields exactly the two sets
magma needs:

| command | reports unused | magma equivalent |
|---|---|---|
| `cargo check` | `dead`, `only_test` | production-reachability set |
| `cargo check --profile test` | `dead` | all-roots (incl. tests) set |

- **dead** = unused in both → production-dead
- **test-only** = unused in prod, used with tests

Structurally identical to the Go oracle (`deadcode` vs `deadcode -test`, test-only = PROD − ALL).
No install required — it is the compiler's own answer, machine-readable via
`cargo check --message-format=json`.

**It handles every hard case correctly.** On a fixture containing `dyn` dispatch, a generic, and
a `macro_rules!`-generated call, the lint did NOT flag `dyn_target`, `generic_target`, or
`macro_target` — all correctly recognised as live.

## 2. Approach comparison

### Candidate A — rust-analyzer as a library (`ra_ap_*`) — CHOSEN

Every capability magma needs exists as a supported API:

| magma need | rust-analyzer provides |
|---|---|
| load a workspace | `load_workspace_at(root, cargo_config, load_config, progress)` → `RootDatabase`; workspaces native via `ProjectWorkspace` |
| macro expansion | `ProcMacroServerChoice::Sysroot` + `load_out_dirs_from_check` |
| call graph | `ra_ap_ide`: `call_hierarchy`, `outgoing_calls`, `incoming_calls` |
| trait / `dyn` dispatch | trait-aware — its own test resolves a call to `S1::callee()` back to trait decl `T1::callee` |
| prod vs test split | ~~`CallHierarchyConfig { exclude_tests }`~~ — **FALSIFIED, see §2a.** Use `Function::is_test`/`is_main`/`is_bench` + two reachability walks (Go's model) |

### 2a. `exclude_tests` FALSIFIED — the prod/test split is not a boolean

Initially recorded as a notable win: magma's Go backend needed two package loads (and got the
distinction wrong twice), while rust-analyzer appeared to expose it as a flag. **That was wrong.**
Verified in two steps on 2026-07-29:

1. With `cfg(test)` off (rust-analyzer's default) there are no test functions in the graph at all,
   so the flag is inert — `exclude_tests` true/false gave identical output.
2. With `cfg(test)` **enabled** via `CargoConfig.cfg_overrides`
   (`CfgDiff::new(vec![CfgAtom::Flag(sym::test)], vec![])`), the fixture's test function and its
   `t → only_test` edge appear (11 functions / 7 edges, up from 10 / 6) — and the two modes are
   **still byte-identical**.

`call_hierarchy.rs` shows why: every `exclude_tests` check is on the **callee** side
(`def.is_test(db)` at lines 83/131/144), filtering test functions out of *results*. It never
suppresses traversal *from* a test function that was explicitly queried, and extraction enumerates
every function.

**Resolution — mirror Go.** rust-analyzer provides the right primitives: `Function::is_test`,
`is_main`, `is_bench`. Enable `cfg(test)`, emit one graph with those flags, and compute
reachability twice (all roots vs production roots). Simpler than the flag, and it supplies
requirement 3's mechanism as a side effect.

Measured build cost (macOS aarch64, stable 1.97.1): **224 packages, 6m50s cold, 2.1 GB target,
0.4s incremental.** Shipped binary: **22 MB release** / 121 MB debug (see §7).

### Candidate B — LLVM IR via stable `cargo rustc --emit=llvm-ir` — REJECTED

Works on stable, and direct call edges are precise (LLVM even emits demangled comments like
`; call fixture::live`). Rejected because:

- **`dyn` dispatch edges are lost.** The call site compiles to a vtable load plus an indirect
  call with no named callee. Recovering the edge means hand-reconstructing RTA from vtable
  contents. This is the *under*-approximating direction — missing edges make live code look dead.
- **IR contains only reachable functions** (`dead` never codegen'd), so the declared set needs a
  second source, and "absent from IR" conflates dead with optimised-away.
- **Monomorphisation multiplies nodes** — one generic becomes N instantiations needing v0
  demangling back to a single source declaration.

## 3. REPRODUCED DEFECT: `outgoing_calls` misses macro-generated edges

Helper output on the fixture (10 functions, 6 edges):

```
main → live → helper
main → gen_call → generic_target
main → speak → dyn_target        <- dyn Trait dispatch RESOLVED correctly
```

- **`main → macro_target` is ABSENT** — 0 edges mentioned it.
- The oracle disagrees: `cargo check` does not list `macro_target` as never-used, i.e. the
  compiler considers it live.
- A graph-derived dead set would therefore contain `macro_target` → **FALSE DEAD CODE**, the
  precise failure magma exists to prevent.

**Fix path (supported, not speculative):** `ra_ap_hir::Semantics` provides
`descend_into_macros{,_cb,_exact,_no_opaque,_breakable}` plus `resolve_method_call`,
`resolve_method_call_fallback`, and `resolve_path`. Walk each function body, descend into macro
expansions, resolve each call/method expression to its target.

(`call_hierarchy.rs` itself contains 27 macro references, so it is macro-aware in some paths; the
gap is specific to outgoing traversal into expansions. Worth an upstream bug report.)

## 4. Rust executes untrusted code — an unavoidable characteristic

Observed directly in the run against a real workspace:

```
[load] build script quote run
[load] build script proc-macro2 run
[load] build script serde run
[load] build script libc run
[load] build script cranelift-isle run
```

These are `build.rs` scripts **being executed**, plus proc macros via the sysroot server.

This is a fundamental departure from Go, where the handoff records: *"Go analysis never executes
target code (type-check + IR only), so the RCE surface is low."*

**It cannot be gated away without breaking parity:**

| disable | consequence |
|---|---|
| build scripts | `build.rs`-generated code invisible (bindgen, protobuf, prost) → missing nodes/edges → false dead code |
| proc macros | derive-generated impls invisible → same failure |

Operator decision: **accepted as an unavoidable characteristic of Rust.** Relative risk note:
installing a pinned toolchain fetches signed official artifacts from static.rust-lang.org;
running `build.rs` executes arbitrary repo-authored code. The larger risk is already accepted.

The handoff anticipated this: *"tree-sitter/other-lang backends that run builds will need the
sandbox+allowlist thinking."*

## 5. Toolchain pinning is an operational failure mode

Sampled real Rust repos:

| repo | pins | installed |
|---|---|---|
| avail | `1.82.0` | no — `cargo metadata` **failed outright** |
| ethrex | `1.91.0` | no |
| roboticus-rust | none | yes |
| architext | none | yes |

**Two of four could not be analysed at all.** The helper did not degrade — `cargo metadata`
errored and produced nothing.

Implications: a pinned toolchain *is* "the language's own toolchain" (not a decision-#3
violation), but it is a hard prerequisite magma cannot satisfy itself. It must surface as an
**honest refusal with an actionable reason**, not a crash — magma already has that machinery;
this is a new refusal reason, not a new mechanism. Note Go is the inverse: `GOTOOLCHAIN=local`
was set specifically to *avoid* toolchain fetching.

## 6. Operator decisions on interaction model

Both gates follow the same pattern (operator, 2026-07-29):

- **Assume interactive runs by default.**
- **Prompt** when the relevant flag is not supplied.
- **Skip** when `--non-interactive` is supplied.

Applies to: (a) toolchain installation, (b) executing target code (build scripts / proc macros).

**Open ambiguity to resolve in the spec:** "skip" must mean **refuse the analysis with an honest
reason**, not "run degraded." A run with build scripts skipped produces false dead code — the
exact failure this bar exists to prevent. Degraded output would violate both full-parity and
honest-refusal principles. Flagged for explicit confirmation.

## 7. Throughput — MEASURED (release build): 43s on a 575k-line workspace

Target: roboticus-rust, 1,288 `.rs` files / 575k lines, workspace, no toolchain pin.

| phase | release | debug | note |
|---|---|---|---|
| `load_workspace` | **2.2s** | 19.2s | build scripts cached from a prior run |
| enumerate | **11.8s** | 206.4s | 3,289 workspace-local functions |
| edges (`outgoing_calls`) | **29.4s** | 484.7s | 4,515 edges |
| **total** | **43.4s** | 710.3s | **release is 16.4× faster** |
| binary | **22 MB** | 121 MB | release is the shipped artifact |

**Build the helper in release mode. This is not a tuning detail — it is the difference between a
viable design and an unusable one.** A debug build measured 11.8 minutes for the same work and
its edge phase failed to complete at all on three earlier attempts.

**The workspace-local filter is essential**: 118,081 functions unfiltered vs **3,289** filtered —
97% of what rust-analyzer loads is dependency and std code. See §7a.

Comparison with Go (different repos, both large real workspaces — indicative, not apples-to-apples):

| | functions | edges | time | per function |
|---|---|---|---|---|
| Go (roboticus) | 17,561 | 49,820 | ~15s | ~0.85ms |
| Rust (roboticus-rust) | 3,289 | 4,515 | 43.4s | ~13ms |

Rust is ~15× slower per function, but **43s in absolute terms is acceptable** for a pre-audit
step, and magma's freshness-skip makes re-runs at an unchanged SHA free. The "near-free to run
before every task" premise survives.

Caveat: the edge count (4,515) is from `outgoing_calls`, which §3 proves **misses macro-generated
edges**. The `Semantics`-based replacement will produce more edges and has **not** been
performance-tested. Its cost profile is unknown and must be re-measured during implementation.

### Remaining unvalidated risks
- **`cfg(test)` is not enabled by default**, so no test functions appeared in the graph at all,
  and `exclude_tests=true`/`false` produced **identical** output. The flag is currently inert and
  the prod/test split is **unproven**. Requires `CargoConfig.cfg_overrides`.
- **Shipped binary size: MEASURED** — 22 MB release (121 MB debug). Materially larger than magma's current pure-Go binary, and must be cross-compiled per target platform.
- **`ra_ap_*` API churn** — all crates at `0.0.343` with no stability guarantee. Operator decision:
  **accepted**; pin the version and update deliberately, because every alternative (nightly `-Z`
  interfaces, user-installed analyzers, a hand-rolled front end) is less secure and less
  maintained. This is a standing maintenance cost the Go backend never had (`x/tools` is stable).

## 7a. The Go backend's correctness facts apply to Rust — carry them as requirements

During this spike the helper reproduced a bug the Go backend had **already found, fixed, and
documented**: it enumerated every crate in the database (118,081 functions on roboticus-rust),
including all dependencies and std, because it never filtered to workspace members. HANDOFF.md
records the identical Go failure:

> **Restrict nodes to the module** (`inModule`/`moduledPath`). Without it the graph includes the
> entire dependency closure (47k nodes, 7.7k false "dead").

rust-analyzer's equivalent filter is `Crate::origin(db)` matching
`CrateOrigin::Local { .. }` ("crates that are workspace members"), imported from
`ra_ap_ide_db::base_db`.

**This was avoidable.** The correctness facts live in a handoff document as prose, so fresh
extraction code does not inherit them. The Rust spec must restate them as explicit, testable
requirements rather than leaving them as tribal knowledge. At minimum, the Rust analogue of each:

1. **Restrict to workspace-local crates** (`CrateOrigin::Local`) — else the dependency closure
   floods the graph and produces false dead code.
2. **Separate production and test reachability** — Go needed two package loads; rust-analyzer
   offers `CallHierarchyConfig { exclude_tests }`, but see §7: it is currently unproven because
   `cfg(test)` is off by default.
3. **Exclude test-declared functions from the test-only view** — Go counted ~9,000 false rows
   before this filter. The Rust analogue must be identified explicitly.
4. **Exclude generated code from both views** — Go had 92 false test-only rows from cgo-generated
   `init` functions. Rust's analogue is build-script- and macro-generated code, which magma will
   now see *because* it executes build scripts (§4).
5. **Refuse when there is no production root** — Go refuses the views when a scope has no non-test
   `main`. Rust's roots are richer (bin targets, lib `pub` API, examples, benches) and the
   equivalent refusal condition must be defined, not assumed.

## 8. Recommended shape for the spec

1. magma ships a Rust helper binary; magma (Go) invokes it as a subprocess and reads JSON.
2. Edge extraction via `Semantics` body-walking **with macro descent** — not `outgoing_calls`.
3. Views derived from the graph (preserving magma's derived-from-graph invariant) **plus a
   runtime cross-check against the `dead_code` lint, failing loudly on disagreement**
   (operator-chosen "more trustworthy path"). The macro defect in §3 is precisely what such a
   cross-check catches.
4. Honest refusals for: missing pinned toolchain, declined execution consent, declined toolchain
   install under `--non-interactive`.
5. Validate set-identically against the lint oracle on real workspaces before claiming parity —
   the Go bar.

## 9. Plan A oracle results (Task 8, 2026-07-29)

`rust-helper/scripts/oracle-diff.sh` (+ `oracle-diff.jq`) is the measuring instrument: BFS
reachability from `root` functions over `calls`, diffed by exact `file:line` key against
`cargo check --message-format=json`'s `dead_code` diagnostics. Direction matters — helper-dead/
oracle-live is FATAL (false dead code), helper-live/oracle-dead is conservative (report-only).
Two fixtures (`libonly`, `collision`) carry FATALs that are honest and adjudicated rather than
bugs (Task 9's residual gap — see below), so `oracle-diff.sh`'s own exit code is not usable as a
pass/fail gate on its own. `rust-helper/scripts/oracle-gate.sh <workspace-root>` (Task 16) is the
gate built on top of it: it diffs the observed FATAL set and SUMMARY counts against a committed
per-fixture baseline (`testdata/<crate>/oracle-expected.json`) and exits 0 only on an exact match,
so a red run is always real signal. Not wired into CI yet (this repo currently has zero Rust CI
jobs of any kind; see `rust-helper/README.md`) but runnable directly against any of the seven
`testdata/` fixtures.

**Fixtures — FATAL = 0 on all three:**

| fixture | total | excluded | FATAL | report-only | agree dead | agree live |
|---|---|---|---|---|---|---|
| `testdata/fixture` | 11 | 1 | 0 | 0 | 2 | 8 |
| `testdata/libonly` | 13 | 2 | 0 | 0 | 5 | 6 |
| `testdata/multi_impl` | 5 | 0 | 0 | 0 | 0 | 5 |

Normalisation excluded 3 functions total across the fixtures: 2 for `test:true` (a `#[test]`
item is never compiled under plain `cargo check`, so the oracle is structurally silent on it —
not the same thing as attribute suppression, but the same class of "the oracle declines to
answer"), and 1 for a newly-identified **trait-impl cascade suppression**: rustc's `dead_code`
lint does not emit a per-method diagnostic for a trait-impl method when its trait *and* self type
are *both* independently already reported dead (verified with 3 isolated `cargo check` probes;
inherent-impl methods get no such treatment). Both exclusion categories were absent from the
brief's illustrative script and were added after the sketch produced non-empty FATAL sections on
two of the three required fixtures — chased to root cause against recorded diagnostics before
trusting the harness, not assumed away.

Attribute-based normalisation (`#[allow(dead_code)]`, `#[no_mangle]`, `#[used]`,
`#[export_name]`) is implemented and separately verified against a throwaway crate (none of the
three required fixtures exercise it): 1 function correctly excluded, 1 sibling genuinely-dead
function correctly still counted `agree_dead`.

**Real workspace (`~/code/roboticus-rust`, 575k lines): NOT MEASURED here, run pending
separately.** The helper phase succeeded — `load_workspace` 1.7s, enumerate 11.0s (8,437
functions), edges 19.4s (12,352 edges, `Semantics`+macro-descent). The oracle phase (`cargo check
--workspace`) did not finish on the first attempt; a corrected framing from a review round:
`target/` was **not actually cold** (3,293 cached `.rmeta` files from prior runs), and a retry
made steady visible progress (~1 crate/second) rather than a from-scratch build. The retry itself
was handed to the coordinator as a separate, independently-run background task
(`/tmp/robo-oracle.json`) rather than completed in this document — real-workspace numbers will be
folded in separately when that lands. Either way, this remains a one-time cost per workspace, not
recurring, once `target/` is warm.

**Critical finding from that real-workspace run — an empty oracle is not a clean one, and the
harness could not originally tell the difference.** The coordinator's own runs against
`roboticus-rust` (warm, after touching every `.rs` file, and after `cargo clean -p
roboticus-core`) all produced the same result: 744 `compiler-artifact`, 77
`build-script-executed`, 1 `build-finished`, **zero `compiler-message`** — no diagnostics of any
kind. `cargo check` only emits diagnostics for crates it actually recompiles; on a warm `target/`
it silently replays cached artifacts and says nothing, which the harness originally read as "the
compiler considers nothing dead" and scored as full agreement — a false green, indistinguishable
from a genuinely clean workspace, for the tool whose entire purpose is making parity measurable.
A naive fix (refuse when total `compiler-message` count is zero) is itself wrong: `testdata/multi_impl`
is a genuinely warning-free fixture and produces zero `compiler-message` entries even on a truly
fresh, cold `cargo check` — message-count alone cannot distinguish "just checked, nothing to
report" from "never re-checked, silently cached," because both look identical on stdout. The fix
uses cargo's own freshness signal instead: `cargo check -v` prints `Checking <pkg>`/`Compiling
<pkg>` to stderr when rustc actually runs, versus `Fresh <pkg>` when it's skipped — verified
directly (identical crate, back to back: `Checking` + a `Running rustc ...` line on a clean build,
`Fresh` with no rustc invocation on the immediate rerun; `compiler-artifact` count was identical
in both cases, so that signal doesn't work either). `oracle-diff.sh` now cross-references every
workspace-local package name (`cargo metadata --no-deps`) against the set that actually shows
`Compiling`/`Checking` this run, and refuses (exit 5, naming the stale packages, with a `cargo
clean` remediation and an honest cost warning — cold checks on a large workspace take minutes to
tens of minutes) if any workspace package was skipped. Verified: all three fixtures fresh
(`cargo clean` then run) still score FATAL=0 as before, including `multi_impl`'s genuine
zero-diagnostics clean pass, which now correctly proceeds instead of refusing; running any
fixture a second time with no clean in between now refuses with exit 5 every time. **Anyone
running this harness against a real workspace with a warm `target/` needs `cargo clean` first, or
the harness will refuse rather than silently hand back a meaningless green.**

**Normalisation, final state:** two disclosed categories (`test:true`, `trait-impl-cascade`,
above) plus attribute suppression. A third, undisclosed heuristic ("test-module-nested" — exclude
any function whose module path contained a `test`/`tests` segment) was tried during
implementation, fired on zero of the three fixtures, and was found by review to silently
over-exclude a genuinely-live function with no `#[cfg(test)]` gate at all (module-name coincidence
only). It was removed rather than kept-and-narrowed: a visible FATAL for an edge case is safer
than an invisible false negative in exactly the mechanism the brief warns divergence hides behind.

**Known blind spot, corrected framing:** Tasks 9 and 11 make the affected functions absent from
the helper's function list entirely (confirmed directly for Task 9: a default-bodied trait method
never appears; confirmed via Task 7's own report for Task 11: `include!`-spliced `OUT_DIR`
functions never reach `enumerate.rs::push`). An earlier draft of this note overstated the
consequence — repro evidence shows **propagated false dead code IS caught**: a live enumerated
function whose only caller is one of these invisible functions still shows FATAL, because the
downstream function is itself a normal node with no incoming edge, so BFS marks it unreached and
the oracle disagrees. What is genuinely uncovered is narrower: the invisible function's *own*
liveness status (no opinion, either direction), and untested chained-invisibility cases. Task 10
has no such blind spot — non-`Adt` self-type impls are enumerated, just mismarked, so a resulting
false-dead-code case surfaces as an ordinary FATAL entry. The harness also now prints a fixed
stderr note on every run naming this limitation and Tasks 9/11 directly, so a bare `FATAL: 0`
cannot be read as "full parity" without it. Full harness design, fixture transcripts, and the
cascade-suppression and blind-spot root-cause chains are in
`.superpowers/sdd/2026-07-29-rust-helper/task-8-report.md`.

## 9. Post-implementation re-measurement — the sysroot bug and its cost

Every performance and coverage number recorded above (§7) was taken **before** a defect found
during implementation: `CargoConfig::default().sysroot` is `None`, so no sysroot was loaded and
std/core `macro_rules!` items (`println!`, `format!`, `write!`, `vec!`, `assert!`, `matches!`)
could not resolve. `Semantics::expand_macro_call` cannot expand a macro it cannot resolve, so
**every call reachable only through a std macro was silently invisible** — in any position.

Fixture tests could not catch this: fixtures use *local* `macro_rules!`, which resolved fine.
Only a fixture requiring `env!`/`concat!`/`include!` exposed it.

Re-measured on roboticus-rust (575k lines) with `sysroot = Some(RustLibSource::Discover)`:

| | before fix | after fix | delta |
|---|---|---|---|
| functions | 8,437 | **10,651** | +2,214 (+26%) |
| edges | 12,352 | **20,268** | +7,916 (**+64%**) |
| load | 1.8s | 80.1s | 44× |
| enumerate | 15.2s | 45.6s | 3× |
| edges | 31.4s | 186.8s | 6× |
| **total** | 48.4s | **312.5s** | **6.5×** |

**The pre-fix graph was missing roughly 39% of all edges.** Every one of those was a potential
false-dead-code source, which is why the fix is not optional under the full-parity bar.

**But §7's conclusion that "48s is acceptable for a pre-audit step" no longer holds.** First-run
cost on a large workspace is now **5.2 minutes**; freshness-skip still makes re-runs at an
unchanged clean SHA free. For comparison, Go maps a similarly-sized repo in ~15s — Rust is now
roughly 20× slower per function.

Two things follow, and both are operator decisions rather than implementation details:

1. The "near-free to run before every task" value proposition needs restating for Rust, or
2. The 80s sysroot load — the single largest new cost, and a fixed overhead independent of repo
   size — needs investigation (it may be cacheable across runs).

### 9a. CORRECTION to §9 — the "80s sysroot load" was a misattribution

§9 reported that the sysroot fix added **80.1s** to the load phase and concluded the sysroot load
needed optimising. **That attribution was wrong**, and the error was mine: I observed the load phase
go from 1.8s to 80.1s after the config change and attributed it to sysroot loading without isolating
the cause — diagnosing from a symptom rather than from a controlled measurement.

A controlled A/B (toggling only the sysroot setting, warm cache) puts the **isolated sysroot cost at
0–6s**, not 78s. Direct re-measurement of the current code on roboticus-rust:

| run | load | enumerate | edges | total |
|---|---|---|---|---|
| 1 | **2.0s** | 14.5s | 115.4s | 131.9s |
| 2 | **2.7s** | 19.3s | 251.5s | 273.6s |

Both runs: 10,651 functions / 20,268 edges — correctness unchanged.

**What the 80.1s actually was:** cold-cache `cargo check` build-script recompilation, which the
workspace load triggers via `load_out_dirs_from_check`. That is a one-time cost per cache state, not
a per-run sysroot cost, and it would occur with or without the sysroot setting.

**Where the real cost lives:** edge extraction (115–251s), which is genuine analysis work — the
+64% edges the sysroot fix made visible have to actually be resolved. That is the price of
correctness, not overhead to be optimised away.

**Second correction — the totals are noisy.** 131.9s vs 273.6s for byte-identical input on
back-to-back runs is a 2.1× spread, caused by other builds competing for CPU on the same machine.
§9's "312.5s" was a single sample presented as a figure. Any future performance claim here needs
repeated runs on an otherwise-idle machine, or it is not a measurement.

Task 14 accordingly shipped **no code change**: there was no 80s sysroot cost to reduce.
