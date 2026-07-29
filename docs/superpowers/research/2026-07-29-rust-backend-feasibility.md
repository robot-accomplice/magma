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
