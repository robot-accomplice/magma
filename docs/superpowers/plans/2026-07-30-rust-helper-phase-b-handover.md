# Rust helper — Phase B handover

> # ▶ START AT THE BOTTOM
> **Jump to "SESSION 2026-07-31 (later still)" at the end of this document.** It is the current state
> and it supersedes everything above it — including the section titled "FINAL HANDOFF", which was
> final only for the session that wrote it. Everything before it is the historical record of how
> the findings were reached: useful for *why*, misleading for *what is true now*.
>
> One-line status: Phase B closed the oracle pipeline, all five contract defects, and **all six
> families A–G** (G found by the post-wiring review), including the `?` and `Drop` desugaring gaps.
> The Rust backend is WIRED. 16/16 rust fixtures pass; Go suite, golangci-lint and clippy green.
> Runtime measured and cut 28%. Adversarial review done (found family G). Rust CI added.
> First Rust artifact VALIDATES CLEAN with Architext.
> **Remaining before release: the Rust CI job has never actually executed, and the structural-debt
> task should land before the next family.** Per the user: **the release does not happen until all
> of it is done.**

**Originally written 2026-07-30 at `df8cba4`. Code complete and wired; Architext-validated at `84fe9b4`.**

Read this with `docs/superpowers/plans/2026-07-29-rust-helper.md` — specifically its final section,
"Plan A outcome: DO NOT SHIP", which is the authoritative finding list. This document is the
operating brief, not a duplicate of it.

> **⚠ Sections below dated to `df8cba4` describe the state BEFORE Phase B.** The instrument is now
> repaired and families A and C are closed. Where this document and the "Phase B progress" section
> disagree, **the progress section wins.** Two specific claims below are superseded: that the
> harness is sound (it had two false-green paths, now fixed), and the instruction not to wire it
> (the user directed "fix it" — wiring happens after families A–F close, not never).

---

## The one-paragraph version

Plan A (17 tasks) built a rust-analyzer-driven helper and an oracle harness whose **gate** is
excellent but whose **oracle pipeline** had two critical false-green paths — it would report green
having measured nothing. The **extraction was unsound**: on a twelve-line idiomatic Rust program
the helper emitted five functions and **zero** call edges, so magma would have reported four dead
functions where rustc reports one. Six independent false-dead-code families were confirmed and
reproduced; the gate was green only because no fixture exercised any of those shapes. Nothing
consumes the helper — Rust repos still refuse — so no user was ever exposed.

**Phase B is fixing all of it before anything is wired.** The instrument is repaired and verified,
families A and C are closed, and all five contract defects are fixed. B, D, E remain; F needs a
different kind of check. Do not soften the "Rust is in development" line until they close.

---

## Decisions already made — do not relitigate

| Decision | Status |
|---|---|
| Rust backend uses rust-analyzer as a library, `ra_ap_*` pinned `=0.0.343` | Settled; the pin is load-bearing, these crates break across patch versions |
| Oracle is rustc's own `dead_code` lint, two configs | Settled and validated; mirrors Go's `deadcode` oracle |
| A lib's `pub` API **is** a root — Go's no-main refusal was deliberately not ported | Settled, in the spec, and verified correct on lib-only crates |
| `fidelity: "semantic"` for Rust | Settled and **confirmed one-sided with Architext** — their schema is `{"type":"string"}`, no enum. Ship freely |
| `executed_target_code` as a contract field | Settled; Architext already declared it (their `code_graph_accepts_magmas_forthcoming_fields` test). **But its value is currently wrong on refusal — see below** |
| Contract governance: new **value** = one-sided, new **field** = two-sided | Settled; caused by `additionalProperties:false` on Architext's root |
| The five load-configuration facts | All present and verified **effective** by an independent reviewer. Do not disturb: both `attach_db` wrappings, `CrateOrigin::Local`, the `cfg(test)` override, `sysroot = Some(RustLibSource::Discover)`, the version pin |

---

## Start here: repair the instrument, THEN fixtures, THEN fixes

**This is the single most important instruction in this document.**

**Step 0 — fix the oracle pipeline (H1–H8 in the plan doc).** Until it is fixed, no measurement
this harness produces can be trusted, including "all 7 fixtures pass" and any before/after the next
phase takes. Two critical false-green paths, both reproduced directly: there is **no guard on
`considered == 0`** (an unmodified fixture copied to a path containing `/build/` compares zero
functions and reports green, because `generated` is an unanchored substring match that excludes
from *both* directions), and **`cargo check`'s exit status is discarded**, so a crate that fails to
compile emits no `dead_code` diagnostics and every function is booked `agree_live`, exit 0.

Note the distinction, because it matters when fixing: **the Task 16 gate is excellent** and
survived all eight attacks made on it (set equality, symbol assertion, duplicate-key rejection,
mandatory reasons, SUMMARY pinning). It is the **oracle pipeline feeding it** that is broken. Do
not rewrite the gate.

Also note that `generated`'s substring heuristic is a *single* root cause with two victims — it
blinds the instrument (H1) and suppresses user-facing dead rows (contract defect). Fix it once.

The gate is green today on a helper that is provably unsound. If the next session fixes defects
first, it will re-earn that same green and have no way to tell whether anything actually improved.
This plan has already been burned three times by exclusions that made red go green while hiding a
real defect; doing it at the scale of six families would be worse.

So: **add one fixture crate per family A–F first, and confirm each turns the gate RED with a FATAL
that matches the family.** A fixture that does not go red is not a fixture, it is a decoration.
Only then start fixing, and re-baseline `oracle-expected.json` per family as it genuinely closes.

Verified as of `755e0b1`: **zero** existing fixtures contain a function used as a value, a desugared
operator call, a const initializer, a deep macro expansion, a `cfg(not(test))` item, or a `derive`.

---

## The six families, in the order I would fix them

Full detail, causes, and file:line are in the plan doc's outcome section. Sequencing rationale:

1. **Contract defects first** — cheap, independent, no architectural risk. Refusal honesty
   (`executed_target_code` lies on its only reachable path), refusal envelopes (two paths emit no
   JSON at all), `test`/`root` semantics (`#[cfg(test)] pub mod` currently becomes a *production*
   root, which hides real dead code), `generated` (unanchored substring match suppresses dead rows),
   `symbol` uniqueness (`(pkg, symbol)` collides; Go carries the receiver type for this reason).

2. **Family E — two package loads.** Matches Go's architecture exactly; Go does two loads precisely
   to avoid the configuration-mixing this helper currently has. Expect roughly double the runtime on
   a run already measured at 130–270s on a large workspace. **Measure before and after — do not
   estimate.** Runtime on this project has been misattributed once already (see the feasibility
   doc's §9a), and back-to-back identical runs were observed to spread 2.1× from CPU contention.

3. **Families A, C, F** — mechanical once located. A: resolve function references used as values,
   not just `CallExpr` with a `PathExpr` callee. C: walk const/static/assoc-const initializers, not
   only `f.source(db).value.body()`. F: stop emitting nodes whose file is inside the rustup
   toolchain (a `derive` expansion is being attributed to `core/src/fmt/mod.rs`).

4. **Family D — the depth-8 macro guard.** Either raise it with evidence or, better, add a
   disclosure channel. Right now a truncated expansion is indistinguishable from one with no calls,
   which is precisely the silent-degradation the refusal design exists to prevent. `serde_json::json!`
   exhausts the budget at the **second key** of a flat object; `println!` alone costs 2 levels.

5. **Family B — desugaring.** The deepest and the one that unblocks everything else. `walk.rs`
   must resolve `BinExpr`/`PrefixExpr`/`IndexExpr` → `ops::*`, `ForExpr` → `IntoIterator::into_iter`
   / `Iterator::next`, `TryExpr` → `From::from`, `AwaitExpr` → `Future::poll`, format args →
   `Display::fmt`/`Debug::fmt`, and scope exit → `Drop::drop`.

### The trap in family B — read before touching `roots.rs`

Trait impls of non-local traits (`Display`, `Iterator`, `Add`) survive today **only by accident**:
`roots.rs:184-196` marks them roots when the trait's name happens to be `use`d into a publicly
reachable module. Root status therefore depends on where an unrelated import sits — moving
`use std::fmt;` from the crate root into a private `error.rs` flips `impl fmt::Display for MyErr`
to false-dead.

The tempting fix is "treat impls of non-local traits as roots." It turns the symptom green while
making **every trait impl a permanent root**, so a dead trait impl could never be reported again —
and it leaves family B's real hole open *and* untestable. Fix the edge layer first; only then
tighten roots to recover reporting power.

---

## What is genuinely good and should not be rewritten

- **The Task 16 gate** (`oracle-gate.sh`/`.jq`) — set-equality against a committed baseline in
  **both** directions (a vanished FATAL fails too), SUMMARY counts pinned, a mandatory written
  reason per baselined FATAL, duplicate-key rejection, and `symbol` asserted on key match. It
  survived 8/8 attacks including the two subtle ones (vanished FATAL; widened exclusion with the
  FATAL set unchanged). **This is the best-built piece of the branch — do not rewrite it.** The
  broken part is the oracle pipeline that feeds it, not the gate.
- **The warm-cache refusal**, and the cascade exclusion's logic — the latter fires only when the
  trait's own name token carries a `dead_code` diagnostic AND the self type is independently dead
  or structurally ineligible, so it provably cannot hide a FATAL. All five baselined reasons
  re-derive exactly against cold `cargo check` output.
- **Determinism verified empirically** — three runs each on three fixtures, byte-identical. Since
  Rust re-seeds `RandomState` per process, that is real evidence no `HashMap` iteration reaches
  the output.
- **`roots.rs` on lib crates.** Verified precise against rustc on an adversarial fixture: re-export
  bridges, private modules, nested `pub mod`, inherent vs trait impls all correct.
- **The extractor/deriver boundary.** `Output` has no reachability field and `Refusal` has no
  functions/calls field, so leaking derived state or emitting a partial graph is *structurally*
  impossible. `model.rs` imports nothing but `serde` — zero `ra_ap_*` types reach the contract layer.
- **Determinism** — sorted edges and output; strictly better than Go's non-deterministic
  `site_line` for multi-site pairs.
- **`SelfType`'s three-state design.** Refusing to collapse a lookup failure into a legitimate
  exclusion is right. (It is arguably in the wrong *place* — see the open question below.)

---

## Open questions the next session should decide (not blockers)

1. **Should harness-only fields leave the wire contract?** `column`, `trait_impl`, and `SelfType`
   exist by their own doc comments to serve `scripts/oracle-diff.sh`, not magma. One reviewer argues
   for a separate `--emit-oracle-keys` output. Weigh against: they are already committed, and moving
   them is churn. Related hazard either way — `column`/`bench`/`trait_impl` have no
   `magma-code-graph/1` counterpart, and Architext's root is `additionalProperties: false`, so if a
   future Go integration passes them through, **every artifact fails validation on day one**.
   Whatever is decided, record at the mapping boundary that they are helper-internal.
2. **Rename the crate.** Still `magma-rust-helper-spike` at version `0.0.0`, `README.md` line 1 still
   says "NOT production", `main.rs:1` still says "Spike:".
3. **`--outgoing` debug mode** writes non-contract `EDGE\t…` lines to stdout with no JSON envelope.
   Keep behind a flag or remove.

---

## Separate, unrelated defect found while validating the Architext bridge

**Not part of this branch. Filed here so it is not lost.** `magma --architext` emits `file` values
that are absolute paths into the Go build cache (`~/Library/Caches/go-build/<hash>/…`) for Go
build-cache stubs — test-binary `main`/`init#1` and cgo shims. 12 such nodes when magma analyzes
itself, 201 for roboticus.

- Every one is `generated: true` (12/12 and 201/201, zero exceptions). **But do not brief consumers
  that this makes them invisible.** That is a property of the data, not of any consumer's filter.
  Architext's visibility predicate is a **union** — `(show_prod_reachable && prod_reachable) || …
  || (show_generated && generated)` — so a node that is *both* `prod_reachable` and `generated` is
  admitted by the first clause and the `generated` toggle never vetoes it. They measured **9 of the
  201 still visible** in their default view (cgo shims like `_Cfunc_free`, each rendering a
  `/Users/jmachen/Library/Caches/go-build/…` path in their inspector). I had asserted the filter
  was complete protection; it is not, and that correction came from them, not from me.
- **Same-machine determinism is intact** — three `--force` runs byte-identical.
- **Cross-machine determinism is not**, because the path embeds a username and a platform-specific
  cache root. `--architext` writes into the repo's own `docs/architext/data/`, which is git-tracked
  in roboticus, so committing it would commit a developer's home directory path. Architext adds a
  second consequence: their layout cache is keyed on `(sha, tree, tier)`, so two developers at one
  SHA producing different bytes would either serve a stale cached layout or thrash it. It does not
  bite today only because their positions derive from graph structure rather than paths.

Architext has been told, has validated both sample artifacts clean, and needs nothing. Samples are
at `~/magma-samples/` (magma `755e0b1`, and roboticus `1e5d69f8` with 23 real dead + 611 test-only
rows — the first non-synthetic exercise of their reachability badges).

---

## State to resume from

- Branch `feat/rust-helper-extraction` at `df8cba4`, clean, all 7 fixtures pass their gate.
- `~/go/bin/magma` was stale (predated the Architext emitter); **reinstalled** from this branch.
  Note both old and new report `0.1.0`, so the version string does not distinguish them.
- Task briefs/reports/ledger: `.superpowers/sdd/2026-07-29-rust-helper/` (gitignored — the durable
  record is the plan doc). The ledger's tail carries two explicit corrections to earlier entries.
- **Not merged, and should not be** until families A–F are closed.

---

# Phase B progress — appended 2026-07-30, after the user directed "fix it"

The user rejected merging this as unwired groundwork ("we're not leaving unwired groundwork in
that state -- wire it up"), and when told that wiring it as-is would emit false dead-code rows,
answered **"Fix it."** So Phase B is being executed, not deferred. Wiring happens only after
families A–F close.

## Closed and independently verified

| Item | Evidence |
|---|---|
| **H1** — `considered:0` reported as green | `testdata/fixture` copied to `/tmp/build/proj` went from `considered:0, excluded:12(generated), fatal:0` to `considered:11, excluded:1(test)`. It now measures. `generated`'s `/target/` test is anchored to the real target dir via `cargo metadata` |
| **H2** — `cargo check` exit status discarded | A crate with a type error now exits **6** (refusal) instead of 0 with every function booked `agree_live` |
| **H3, H4, H6, H7, H8** | Doc-comment prose no longer matches the attribute scan; oracle widened to `--all-targets`; exit-3 refusal path reachable; crate-level `#![allow(dead_code)]` disclosed; `executed_target_code` asserted and pinned in every baseline |
| **H5** — no test-profile oracle | Now two configs: production-roots BFS vs plain `cargo check`, and all-roots BFS vs `--profile test`. `test:true` no longer excluded from the test direction. Found and fixed a real trap: `--all-targets` already triggers a cfg(test) recompile, so a naive second invocation saw `Fresh` and false-refused on every fixture |
| **Fixtures A–F** | Six added. Five reproduce as real FATALs; family F does not (see below) |
| **Family A** — function-as-value | `walk.rs` resolves function-valued `PathExpr`s, not only `CallExpr` callees. Emitted as `dynamic` — address-taken ≠ called, over-approximating toward "live" |
| **Family C** — initializer calls | Closed via **synthesized `init` nodes**, following Go's `init#N` precedent (verified: magma's own graph has 8). `{qualified}#init`, `kind:"init"` (new *value* = one-sided), rooted. `fn_value` and `const_init` both at `fatal:0` — verified by me, not just by a gate PASS |
| **Contract defects 1–5** | `executed_target_code` honest per refusal site; every soft-refusal path emits `computable:false` JSON and Go's exit convention adopted (0=refusal, 2=usage); `test` derived from cfg-ancestry + target kind so `#[cfg(test)] pub mod` no longer becomes a production root; `generated` no longer swallows hand-written `macro_rules!`; `symbol` qualified (`<T as Trait>::m`) so `(pkg,symbol)` is unique |

**Behavioural change, and the standing principle behind it.** Fixing `test` derivation means a
crate whose only `pub` items live under `#[cfg(test)]` now **refuses** ("no roots in scope") where
it previously reported a confident production root. That crate has no production entry point, so
there is no production reachability to compute. **This is the designed behaviour, not a cost.**

> **Mapping every crate is not the goal.** magma's job is an honest map or an honest refusal.
> Refusal rate is not a quality metric and must never be optimised down. A refusal is a real
> answer — the Go path already works this way, and the magma skill documents it.

This principle is load-bearing for the rest of Phase B, because every remaining family creates the
same temptation: **widen roots until things stop looking dead.** That is the "treat impls of
non-local traits as roots" trap called out for family B — it turns a symptom green while making
every trait impl a permanent root, destroying the tool's ability to ever report a dead trait impl
again. Any fix that raises the number of mapped crates or lowers the number of dead rows *by
widening roots rather than by finding real edges* is moving backwards, however green it looks.
Fix the **edge layer**; let roots stay tight; let honest refusals happen.

## Still open

- **Family B (desugaring)** — in flight. The deepest, and the one that unblocks tightening roots.
- **Family E (two loads)** — in flight. `cfg_test_global` still cannot gate without an artificial
  test-caller workaround; the single permanently-cfg(test)-on load is the root cause.
- **Family D (depth-8 macro guard)** — not started. Owns `walk.rs`, so it is queued behind B.
- **Family F (toolchain nodes)** — **needs a different check entirely.** Confirmed real as a
  metadata defect (`#[derive(Debug)]` resolves a node into `~/.rustup/.../core/src/fmt/mod.rs`),
  but rust-analyzer marks that node `root:true` regardless of visibility, so a *reachability*
  comparison can structurally never disagree with rustc about it. The right check is an assertion
  that no node's `file` falls outside the workspace root — not a fixture.
- Contract-task residuals: `cfg_requires_test` does not handle `any(test, …)`/`not(…)`;
  `tests/`/`benches/` detection is path-component-based rather than real Cargo target metadata;
  the type-error refusal's per-file diagnostic scan is unmeasured on a large workspace.
- **The gate is green with open defects recorded as such.** Baselines carry a per-entry
  `status: "open-defect"` with a reason that explicitly distinguishes them from `libonly`/
  `collision`'s permanent adjudications. Do not let those two categories blur.

## Correction to this document's own earlier claim

An earlier revision said every absolute-path node being `generated:true` meant filtering
`generated` removed them completely. True of the data, **false as a statement about consumers** —
Architext's visibility predicate is a union, so `prod_reachable && generated` is admitted before
the `generated` toggle can veto it. They measured 9 of 201 still visible. Brief consumers on the
data property and the filter property separately.

## Architext status — closed, nothing outstanding

Both sample artifacts validated clean. The badge reconciliation is **exact**: their predicates and
magma's views were written independently and both land on **23 dead / 611 test-only** across
17,871 real roboticus functions, including the non-obvious `10,011 → 799 → 611` chain. That is the
strongest evidence to date that `magma-code-graph/1` is unambiguous. `fidelity` is confirmed
one-sided (their schema is `{"type":"string"}`, no enum) so `"semantic"` needs no coordination.
They have been told no Rust artifact is coming soon, and that a real emit will be sent for
validation before anything is wired — a process now three-for-three at catching defects pre-ship.

---

# FINAL HANDOFF — 2026-07-31, at `c076d3a` (66 commits ahead of `develop`)

**Everything above is history. This section is the current state. Start here.**

Branch `feat/rust-helper-extraction`, tree clean, build green, **13/13 fixtures pass cold**,
`libonly` and `collision` still at exactly 2 and 3 adjudicated FATALs (no exclusion widened
anywhere across all of Phase B — checked after every task).

**Release gate, stated by the user: nothing ships until all remaining work below is done.**
Not the wiring, not a PR, not a change to the "Rust is in development" line.

## Phase B outcome — all six families addressed

| Family | State | Notes |
|---|---|---|
| **A** function-as-value | ✅ closed | `walk.rs` resolves function-valued `PathExpr`s, not only `CallExpr` callees. Emitted `dynamic` — address-taken ≠ called, over-approximating toward "live" |
| **B** desugaring | ⚠️ **5 of 6** | Closed: operators (`+`/`-`/`[]`), `await`, `for`-loops, format-args. **Open: `?` and `Drop`** — see below |
| **C** initializer calls | ✅ closed | Synthesized `init` nodes on Go's `init#N` precedent. `{qualified}#init`, `kind:"init"` (new *value* → one-sided), rooted |
| **D** macro depth | ✅ closed | Limit 8→**64** on measured evidence; new `macro_truncated: bool` disclosure field |
| **E** two loads | ✅ closed | Two workspace loads (cfg(test) off/on), merged on `(file, line, column, symbol)` identity |
| **F** toolchain nodes | ❌ **not closed** | Needs a different kind of check entirely — see below |

Also closed: the **oracle pipeline** (H1–H8) and all **five contract defects**. Both verified
independently, not taken on report.

## The three things that remain

### 1. Family F — needs a workspace-root assertion, NOT a fixture

`#[derive(Debug)]` makes the helper emit a node whose `file` is inside
`~/.rustup/toolchains/…/library/core/src/fmt/mod.rs`. It is real and reproducible. **But a
reachability comparison can structurally never catch it**: rust-analyzer marks that node
`root:true` regardless of actual visibility, so the helper and rustc can never disagree about it,
and no oracle fixture will ever go red. `testdata/toolchain_derive` exists and documents this; it
is *not* coverage and must not be mistaken for it.

The right check is a **direct assertion that no emitted node's `file` falls outside the workspace
root**, run as a test or a harness pre-flight. Cheap, and it closes the family properly.

### 2. `?` and `Drop` — the honest residue of family B

- **`expr?`** — `Try::branch` resolves, but the load-bearing `FromResidual` → `From::from`
  conversion **has no exposed API at `ra_ap_* = 0.0.343`**. Confirmed still open by probe
  (`<MyErr as From>::from` reads dead before and after). Options: a targeted type-directed lookup
  the way format-args was solved, or a pin bump — the pin is load-bearing, so bumping it is its own
  task with its own re-validation.
- **`Drop::drop`** — no expression node exists for scope exit at all; catching it needs
  move/liveness analysis this walker does not do. The implementer declined to ship an unverifiable
  heuristic, which was correct.

Both are **false-dead-code sources that remain open**. `?` in particular is extremely common in
real Rust. Do not wire without deciding explicitly what to do about them.

### 3. magma-side wiring, then a fresh adversarial review

Nothing is wired yet: only `detect.Go` is registered, nothing `exec`s the helper, Rust repos still
refuse. Wiring means registering a Rust backend, invoking the helper, mapping
`magma-rust-helper/1` → magma's internal graph, and deciding what `fidelity` and
`executed_target_code` carry through.

**Then re-run the full three-lens review.** The last one found six false-dead-code families that
three tasks' worth of green gates had completely missed. A green gate is necessary, not sufficient.

**Boundary hazard to honour when mapping:** `column`, `bench`, `trait_impl`, and now
`macro_truncated` and `kind:"init"` exist on `magma-rust-helper/1` (helper→magma, internal). The
Architext-facing `magma-code-graph/1` sets `additionalProperties: false`, so passing any *new
field* through fails validation on day one. New **values** (like `kind:"init"` or
`fidelity:"semantic"`) are one-sided and fine. Record the strip-list at the mapping boundary.

## Standing principles — these were learned expensively

> **Mapping every crate is not the goal.** An honest map or an honest refusal. Refusal rate is not
> a quality metric and must never be optimised down.

> **Never widen roots to make something green.** Every remaining family creates that temptation.
> "Treat impls of non-local traits as roots" would have turned family B green while making every
> trait impl a permanent root — destroying the ability to ever report a dead trait impl. Family B
> correctly declined to tighten `roots.rs` at all, because format-args and `for`-loop resolution
> only cover concrete-`Adt` types; `.to_string()`, `dyn Trait` format args, and generics are still
> uncovered. **Roots may only be tightened after those close.**

> **Add no exclusion, ever.** Four exclusions in this harness have now been found unsound. If a
> divergence is genuinely explainable it becomes a *disclosed, baselined FATAL with a written
> reason* — never a silent exclusion. Baselines carry per-entry `status: "open-defect"` vs
> permanent adjudication; do not let those blur.

> **One sample is not a measurement.** Family E measured the *identical* config at 176.7s and
> >633s on repeat (>3.6× spread under load) and correctly refused to publish a "2×" figure. The
> project had already been burned once by presenting one sample as a number (feasibility §9a).

## Process lesson — use worktree isolation for parallel agents

Several agents sharing one live branch cost real work twice: one task's uncommitted `walk.rs`
edits reverted to an earlier snapshot mid-session, and **my own `git add -A` swept 514 lines of
another task's in-flight `main.rs` into an unrelated docs commit.** File-ownership conventions kept
the *content* from colliding but do not protect uncommitted work from a shared index.

**Next time: give each parallel agent its own git worktree.** If that is not available, require
every agent to commit before yielding and never run `git add -A` from the orchestrator.

That history was split with the user's approval — the docs commit and the source commit are now
separate and accurately titled. **Backup ref `backup/pre-split-105894b` (`9f3523f`) is still
present**; tree hashes were verified identical before and after, so no content moved. Delete the
ref once satisfied.

## Unmeasured / deferred, with reasons

- **Runtime of the 8→64 macro limit** — never measured; its timing run was killed when I told
  family D to stop and commit ahead of a concurrent task. My call, recorded as unmeasured rather
  than estimated.
- **Runtime of two-load on a real workspace** — inconclusive at achievable sample size under load.
- **`--all-targets` + package-scoped clean cost** — the test-config oracle recompiles workspace
  packages a second time; measured only on the 13 small fixtures.
- **Pre-existing target-dir anchoring gap** for excluded nested sub-projects (a `cargo-fuzz`
  crate) causes a refusal on `roboticus-rust`. Unrelated to Phase B, unfixed, needs its own task.
- **`cfg_test_global`'s `tests::uses_it` workaround is still required** — not for the original
  reason (the merge makes it unnecessary for correctness) but because removing it reopens a
  masking hole in `oracle-diff.sh`'s own `--all-targets` pollution.
- **Contract residuals**: `cfg_requires_test` does not handle `any(test, …)`/`not(…)`;
  `tests/`/`benches/` detection is path-component-based rather than real Cargo target metadata.

## Separate, unrelated: the Go-side path leak

`magma --architext` emits `file` values that are absolute paths into the Go build cache for
build-cache stubs (12 nodes analyzing magma itself, 201 for roboticus; all `generated:true`).
Same-machine determinism is intact (three `--force` runs byte-identical); **cross-machine is not**,
since the path embeds a username and platform-specific cache root — and `--architext` writes into
git-tracked `docs/architext/data/`. Architext confirmed 9 of 201 remain visible in their default
view because their filter is a union, so `generated:true` does **not** mean hidden. Needs its own
task on the Go side.

## Architext — closed, nothing outstanding

Both sample artifacts validated clean (`~/magma-samples/`). Badge reconciliation is **exact**:
their predicates and magma's views, written independently, both land on **23 dead / 611 test-only**
across 17,871 real roboticus functions, including the non-obvious `10,011 → 799 → 611` chain.
`fidelity` confirmed one-sided (`{"type":"string"}`, no enum). They know no Rust artifact is coming
soon and that a real emit will be sent for validation before wiring — a process now three-for-three
at catching defects pre-ship (`tree` carrying the SHA, `executed_target_code` as a new property,
and the union-filter correction).

---

# SESSION 2026-07-31 (later) — families F and B fully closed

**This section supersedes the FINAL HANDOFF above.** Two commits on
`feat/rust-helper-extraction`: `d042e3e` (family F) and `23d2a95` (family B's `?` and `Drop`).
**15/15 fixtures pass**, determinism re-verified (three runs each on three fixtures,
byte-identical). No exclusion was widened to make anything green except the one deliberate,
measured case recorded below.

## Family F — closed, and it was worse than filed

Filed as a metadata blemish. Reproducing it from actual output first (rather than fixing from the
description) surfaced two further defects:

1. **Every builtin derive**, not just `Debug`. A six-derive probe put 7 of 9 nodes in the sysroot —
   `clone.rs`, `cmp.rs` (twice), `default.rs`, `hash/mod.rs`, `fmt/mod.rs`.
2. **Silent node loss and fabricated cross-crate edges.** The resolved location is the *trait's own
   method declaration* — `core/src/fmt/mod.rs:1084` is literally
   `fn fmt(&self, f: &mut Formatter<'_>) -> Result;` — so it is a per-trait CONSTANT shared by every
   deriving type in the workspace. Two crates each with `#[derive(Debug)] pub struct Widget`
   produced byte-identical `node_key`s: one node vanished in `merge_configs`, and
   `remap_and_aggregate` rewrote the survivor's id over both, giving
   `EDGE a::show -> b::<Widget as Debug>::fmt` where crate `a` formats its *own* `Widget`.
   Reproduced on a two-crate probe.

Root cause for all of it, verified not inferred: rust-analyzer resolves a builtin-derive method's
`source(db)` to the sysroot trait declaration with `macro_file()` returning `None`, so the item
looks like an ordinary file to every macro-kind check — which is also why `generated` read `false`.

**Fixes.** `enumerate::relocate_out_of_root` moves an out-of-root node onto local source (impl
block, else the self type's declaration), accepting a candidate only if it lands inside the
workspace. Per-type rather than per-trait, so the collision dissolves at source. `pkg` added to
`main::node_key` as defence in depth. `main::non_local_paths` **refuses** the run if any emitted
`file`/`site_file` still escapes the workspace root — a reachability fixture can structurally never
catch this class, so a direct assertion is the only instrument that sees it, and refusal (not
filtering) because a drop would be a silent exclusion.

**The one deliberate exclusion-boundary movement in Phase B.** Relocated nodes now report
`generated: true`, which moves them from `considered` into `excluded`. It cannot hide a FATAL:
measured on a probe whose `Widget` derives Debug/Clone/PartialEq/Default/Hash/PartialOrd with every
one unused, `cargo check --message-format=json` reports **zero** `dead_code` diagnostics. rustc
structurally cannot flag a derived impl dead, so those nodes were permanently `agree_live` before
and are permanently un-flaggable after. `toolchain_derive`'s baseline carries the measurement.

## Family B — `?` and `Drop` both closed

Each got a fixture **confirmed RED first** by parking the fix and rebuilding, never by inference.
Both closed by finding real edges: `excluded` is 0 in both fixtures.

- **`?` (`testdata/try_from`, was 1 FATAL both configs).** Closed type-directedly, the same route
  format args takes. `edges()` computes the enclosing function's error type once
  (`try_error_type`, gated on the return type actually being `core::result::Result` resolved
  through the crate graph, never name-matched) and threads it into `walk`. **No pin bump was
  needed** — `Type::type_arguments` exists at `0.0.343`, so the handover's "targeted lookup or bump
  the pin" resolves to the first option, and the pin stays load-bearing and untouched.

- **`Drop` (`testdata/drop_glue`, was 4 FATALs both configs).** **The previous reading was wrong
  and this is worth keeping.** It was recorded as catchable only by move/liveness analysis, because
  no FATAL seemed producible. But rustc's `dead_code` lint never reports a trait-impl method at
  all, so it says nothing about `drop` either way — what it DOES report is drop's *transitive
  callee*. Measured on a probe carrying both a constructed and a never-constructed `Drop` type,
  rustc emitted `struct NeverConstructed is never constructed` and `function cleanup_never is never
  used`, and stayed silent about both `drop`s. The divergence is observable one hop past `drop`.

  Closed by resolving from the TYPES occurring in a body. **Transitivity is the load-bearing
  half**: a `#[derive(Default)] struct Outer { inner: Inner }` with `Inner: Drop` puts `Inner` in no
  expression anywhere in the program, yet `inner_cleanup` genuinely runs and rustc agrees it is
  live — measured on a probe, then folded into the fixture. `drop_glue_map` expands each ADT
  through its owned field types (following generic arguments, so a `Vec<Inner>` field yields
  `Inner`) with a `seen` set for cyclic types. Crates with no local `Drop` impl get an empty map and
  skip the per-expression type lookup entirely.

## What remains

1. **magma-side wiring.** `detect.Rust` and `Cargo.toml` detection already exist; only
   `internal/backend/rust.go` is missing. **Decided this session: PATH lookup with honest refusal**
   — the backend looks for the helper on `PATH` (or `MAGMA_RUST_HELPER`) and refuses with an
   install hint when absent. Keeps `go install` working and needs no release-pipeline change.
   - **`README.md:49-50` must change.** It currently claims "The only runtime requirement is a
     working `go` toolchain … no third-party analyzer binaries to install." True for Go, false the
     moment Rust is wired. Scope the claim to Go and state the Rust helper requirement.
   - **The strip-list is enforced structurally, not by convention** — `contract.Node` has no
     `column`, `bench`, `trait_impl`, or `macro_truncated` field, so those cannot leak to Architext.
     Only `kind: "init"` reaches the wire, and a new *value* is one-sided. Confirm at review.
   - **`Reachable`/`ProdReachable` are magma's to compute, not the helper's.** Go derives them from
     `rta.Result`, so there is NO reusable BFS in `internal/contract` — the Rust backend needs its
     own, over `root && !test` for prod and all roots for total.
2. **A fresh three-lens adversarial review.** The last one found six families that three tasks'
   worth of green gates had missed. This session found three more defects that a 13/13 green gate
   had also missed. A green gate is necessary, not sufficient.

## Open residuals — carried forward, none newly introduced

- **`root`/`exported` over-report for derived methods.** rust-analyzer reports a derive-synthesized
  method's own Visibility as Public even for a fully private type, tripping `roots.rs`'s
  `all_ancestors_public` fallback. Both fail toward live. **Not fixed here deliberately**: roots may
  not be tightened while format-args and `for`-loop resolution still cover only concrete-`Adt`
  types.
- **A genuinely-dead trait-impl method always reads "live" to this harness**, because rustc's
  `dead_code` never reports trait-impl methods. General to trait impls, not specific to `Drop`.
  `testdata/drop_glue` deliberately omits a never-constructed `Drop` type for this reason.
- **`cargo fmt` is not clean** across `roots.rs`/`walk.rs`/`main.rs`/`enumerate.rs` — pre-existing,
  and CI gates Go only (`gofmt`), never Rust. **Rust has no CI coverage at all**; it should get some
  as part of wiring.
- Contract residuals unchanged: `cfg_requires_test` does not handle `any(test, …)`/`not(…)`;
  `tests/`/`benches/` detection is path-component-based rather than real Cargo target metadata.
- **Pre-existing target-dir anchoring gap** for excluded nested sub-projects (a `cargo-fuzz` crate)
  causes a refusal on `roboticus-rust`. Unrelated, unfixed, needs its own task.
- **Go-side path leak** (`--architext` emitting build-cache absolute paths) still needs its own task.

---

# SESSION 2026-07-31 (later still) — Rust backend WIRED

**Supersedes the section above.** Commit `bd33377`. magma now maps a Cargo workspace end-to-end
(verified on `testdata/try_from`: 4 nodes, 3 edges, 0 dead — matching the helper's own output).
15/15 rust fixtures pass, full Go suite passes, `golangci-lint` 0 issues, `gofmt` clean.

## Delivery: PATH lookup with an honest refusal

Decided with the user. `internal/backend/rust.go` finds `magma-rust-helper` on `PATH` or at
`$MAGMA_RUST_HELPER`, and REFUSES with the install command when absent — not an error, not a silent
skip. Keeps `go install` working and needs no release-pipeline change. The alternative (shipping a
Rust cross-compile matrix in `release.yml`) still needs this fallback anyway, so it can be added
later without rework.

`README.md` updated on both counts: the "no third-party analyzer binaries to install" claim was
true for Go and false the moment Rust is wired, and "v0.1.0 supports Go only" was stale.

## The contract gap wiring exposed — this one would have shipped a wrong map

`roots::mark` sets `root` for PRODUCTION entry points only. The narrow "carries a literal
`#[test]`" signal existed **nowhere on the wire** — only as a regex over source text inside
`scripts/oracle-diff.sh`. So a consumer reading only `magma-rust-helper/1` had no correct option:

- seed all-roots on `root` → all test-only code reads dead;
- seed on `test` → `test` is deliberately broader (cfg(test) ancestry, `tests/`/`benches/`
  targets), so every function under a test module becomes its own root, trivially reaches itself,
  and a genuinely dead test helper can never be reported. The exact false-agreement failure the
  oracle harness exists to catch.

`f.is_test(db)` was already computed inside `is_test_context` and collapsed into its broad OR. Now
emitted as **`test_entry`** — a new FIELD on the internal helper→magma contract only, not on the
Architext-facing `magma-code-graph/1`, so one-sided and needing no coordination.
`TestRustReachabilitySeedsOnTestEntryNotTest` was confirmed to FAIL on the `test`-seeded version
before being accepted.

**Follow-up, deliberately NOT done here:** `oracle-diff.sh` can now drop its `#[test]` regex scan
and read `test_entry` instead, replacing a text heuristic with rust-analyzer's own attribute
resolution. Deferred because it modifies the MEASURING INSTRUMENT, which should change in a
dedicated task with its own verification rather than as a side effect of wiring — the gate is the
best-built piece of this branch and the handover's standing advice is not to disturb it casually.

## Also fixed

- **`kind:"init"` counted nowhere.** Family C's initializer nodes matched no case in
  `notes/metrics.go`, so `Funcs+Methods < len(Nodes)` on every Rust map. Fixed in `metrics.go`
  rather than flattened at the mapping boundary — Go's own `init#N` nodes already arrive as
  `"func"`, so both languages now tally the same thing.
- **`.gitignore` failed open.** It listed each fixture's `target/` on its own line; adding
  `try_from` and `drop_glue` without adding two more lines tracked 31 files of build output.
  Replaced the hand-maintained list with `/rust-helper/testdata/*/target/`.

## Strip-list: enforced by the type, not by convention

`contract.Node` has no field for `column`, `bench`, `trait_impl`, `macro_truncated`, or
`test_entry`, so none can reach Architext, whose root is `additionalProperties: false`.
`TestRustHelperInternalFieldsAreStripped` pins it. `bench` is consumed (it seeds all-roots
alongside `test_entry`) rather than discarded.

## RUNTIME — the open item, and what is and is not known

**Known:** one config of the two on `roboticus-rust` (1388 `.rs` files, 12 `Drop` impls) took
**769.8s**, measured, on a run CONTENDED with the fixture suite.

**Not known, and not to be guessed at: whether that is a regression.** There is no matched
before/after. The handover already records that Family E measured the *identical* config at 176.7s
and >633s on repeat — a >3.6× spread under load — so a contended 769.8s is **not distinguishable
from existing baseline variance**. Do not report it as a slowdown, and do not report it as fine.

**The specific risk, which is real and identified.** The Family B drop arm calls
`sema.type_of_expr` on EVERY expression node, in any crate with at least one workspace-local `Drop`
impl. Crates with none skip it entirely (`drop_glue_map` returns empty and `walk` checks that
first), so the common case is free — but `roboticus-rust` has 12, so it is active there.

**The measurement to run, on a QUIET machine:** three runs each of one config on `roboticus-rust`,
with and without the drop arm (a one-line early return in `drop_glue_map` isolates it exactly),
comparing medians. Three runs, not one — this project has been burned twice by presenting a single
sample as a number. If it does prove costly, the mitigation is cheap and already scoped: restrict
the arm to the expression forms that actually PRODUCE values (`CallExpr`, `MethodCallExpr`,
`RecordExpr`, `PathExpr`) instead of every expression node. That was deliberately not applied blind.

## What remains

1. **The runtime measurement above.**
2. **A fresh three-lens adversarial review.** Now more clearly warranted, not less: this session
   found FOUR defects that a 13/13 green gate had missed — family F's silent node loss and
   fabricated cross-crate edges, the `Drop` gap being observable after all, the `test_entry`
   contract gap, and `kind:"init"` counted nowhere. A green gate is necessary, not sufficient.
3. **Rust has no CI coverage.** `.github/workflows/ci.yml` gates `gofmt` and Go tests only. Neither
   `cargo build`, the fixture gate, nor `cargo fmt` runs in CI — and `cargo fmt --check` is
   currently NOT clean across `roots.rs`/`walk.rs`/`main.rs`/`enumerate.rs` (pre-existing).
4. **A real Rust emit should go to Architext for validation before release** — that process is
   three-for-three at catching defects pre-ship.

---

# Real-workspace validation on `roboticus-rust` — measured, 2026-07-31

First full run of the wired pipeline against a real workspace. **It did not refuse** — see the
correction below.

```
TIMING prod-config (cfg(test) off): 769.8s
TIMING test-config (cfg(test) on):  918.7s
TIMING merge: 0.2s for 10921 functions, 24611 calls
TIMING TOTAL: 1688.8s          (real 1689.45  user 1298.74  sys 62.06)
```

10,921 functions, 24,611 calls, an 11 MB JSON envelope, exit 0.

## Correction: `roboticus-rust` no longer refuses

An earlier section records "a pre-existing target-dir anchoring gap for excluded nested
sub-projects (a `cargo-fuzz` crate) causes a refusal on `roboticus-rust`." **That is now stale** —
this run produced a complete graph. Do not plan around a refusal there. Whether the gap was fixed
incidentally or the original diagnosis was wrong is NOT established, so treat the refusal claim as
withdrawn rather than as resolved.

## Family F validated at scale

Across 10,921 nodes and 24,611 call sites: **zero** absolute `file` paths, **zero** absolute
`site_file` paths, **zero** paths mentioning `rustup`/`.cargo`. `main::non_local_paths` did not
fire, which is the correct outcome rather than a silent one — it would have refused the whole run.
2,095 nodes `generated: true` (~19%, consistent with derives being relocated and correctly
flagged), 274 `kind:"init"` nodes (family C), 3 `macro_truncated` (family D's disclosure firing on
real code).

## Why `test_entry` was genuinely blocking — quantified on this data

**5,574 of 10,921 functions (51%) carry `test: true`.** Reachability computed offline from this
exact graph, both ways a consumer could have seeded it before `test_entry` existed:

| all-roots seed | seeds | reachable | UNREACHABLE (= reported dead) |
|---|---|---|---|
| `root` only | 3,707 | 5,042 | **5,879** |
| `root \|\| test \|\| bench` | 9,281 | 10,723 | **198** |

The two wrong answers differ by **5,681 nodes** — a ~29× difference in the dead-code count. And
**294 of those nodes reach nothing at all**: they are live only because the broad seed made them
their own roots. Those are precisely the genuinely-dead test helpers that could never have been
reported. The correct seed (`root` for prod, `root || test_entry || bench` for all-roots) sits
between the two, and neither wrong option is a conservative approximation of it.

## Runtime — what is now known, and what still is not

**Known:** 1688.8s (28.1 min) end to end, on a run that was contended for part of its life. Nearly
all of it is the two workspace loads (769.8 + 918.7 = 1688.5s); the merge is 0.2s and is not worth
optimising.

**Still not known: how much of that the family B drop arm costs.** The per-config TIMING covers
load AND walk together, so the walk cannot be separated from the load in this data, and there is no
matched before/after. Do NOT read 1688.8s as a regression — the handover already records the
identical config measured at 176.7s and >633s on repeat (>3.6× spread under load).

**What to do about it, in order:**
1. **Instrument first.** Split the per-config TIMING into load vs walk so the question is
   answerable from one run instead of two. Cheap, and it makes every future measurement cheaper.
2. Then, on a quiet machine, three runs each with and without the drop arm (a one-line early return
   in `drop_glue_map` isolates it exactly). Three, not one — this project has been burned twice by
   presenting a single sample as a number.
3. Only if it proves costly: restrict the arm to value-producing expression forms (`CallExpr`,
   `MethodCallExpr`, `RecordExpr`, `PathExpr`) rather than every expression node. Deliberately not
   applied blind.

**Separately worth deciding:** 28 minutes is a real UX fact for a magma run on a large Rust repo,
independent of any regression. Live progress now reports through (commit `887effc`), so it no
longer LOOKS hung, but the wall-clock cost should be stated in the README before release.

---

# RUNTIME ANSWERED — measured 2026-07-31, `roboticus-rust`, instrumented

The phase instrumentation (`de1a806`) answered this in one run, and **the answer falsifies the
hypothesis this document was carrying.** Recorded here because the wrong guess is instructive.

```
prod-config (cfg(test) off) 678.8s      test-config (cfg(test) on) 1021.7s
  load                 7.1s               load                 5.9s
  enumerate+roots     56.1s (5110 fns)    enumerate+roots     48.9s (10651 fns)
  diagnostics        549.1s               diagnostics        863.2s
  walk (bodies)       62.0s (11026 e)     walk (bodies)       95.0s (24629 e)
  walk (initializers)  0.1s               walk (initializers)  0.2s
merge 0.2s     TOTAL 1700.8s
```

| phase | total | share |
|---|---|---|
| **diagnostics** (`workspace_type_errors`) | **1412.3s** | **83.0%** |
| walk — ALL of family B's desugaring | 157.0s | 9.2% |
| enumerate+roots | 105.0s | 6.2% |
| load | 13.0s | 0.8% |
| merge | 0.2s | 0.0% |

## The drop arm was the wrong suspect

This document previously named family B's drop arm (per-expression `type_of_expr`) as "the
specific risk, which is real and identified". **That was a hypothesis, and it is now bounded out
of relevance.** The ENTIRE walk phase — every desugaring form, operators, `for`, `await`, format
args, `?`, and the drop arm together — is 9.2% of runtime. The drop arm is some fraction of that.
`roboticus-rust` has 12 local `Drop` impls, so the arm was genuinely active; it is simply not where
the time goes.

**Do not apply the scoped mitigation** (restricting the arm to value-producing expression forms).
It would cost precision and buy at most a few percent. Left unapplied deliberately.

## The real cost is the type-error diagnostic scan

`main::workspace_type_errors` calls `Analysis::full_diagnostics` per workspace-local `.rs` file, in
BOTH configs. That is 83% of a 28-minute run. It was added as contract defect 2's fix — refuse
rather than emit a partial map when a workspace does not type-check — so it is correctness-critical
and must not simply be deleted.

This document previously listed "the type-error refusal's per-file diagnostic scan is unmeasured on
a large workspace" as an open residual. **It is now measured, and it is the dominant cost.**

**Leads, not conclusions — none of these has been tried:**
1. `load_out_dirs_from_check: true` already runs `cargo check` during load. If cargo's own
   diagnostics can be captured there, the separate RA pass may be redundant or much cheaper.
2. It runs in both configs. The two are not identical (cfg(test) code only exists in one), but the
   prod-config file set is largely a subset — scanning only what the second config adds could save
   most of the 863.2s half.
3. `full_diagnostics` computes every diagnostic; only `Severity::Error` is consulted. A narrower
   query, if one exists at the pin, would avoid computing what is then discarded.
4. It is a per-file loop with no parallelism, on a machine that showed `user 1286s` against
   `real 1701s` — i.e. largely single-threaded.

## Method notes

Two independent full runs, **1688.8s and 1700.8s — 0.7% apart**, despite the first being contended
with the fixture suite. That is a genuinely reproducible total, and it retires the earlier concern
that contention made the first number unusable. It does NOT retire the standing rule that one
sample is not a measurement; it means these two agree.

Node/edge counts also reproduced exactly across both runs (10,921 functions, 24,611 calls), which
is independent evidence for the determinism claim on a real workspace rather than a fixture.

## `test_entry` — the payoff, measured on the wired pipeline

Re-run with `test_entry` present (5,239 true, vs 5,574 for the broad `test` — the 335 difference is
exactly the test-context-but-not-`#[test]` helper population). All three seeds over the same real
10,921-function graph:

| all-roots seed | seeds | unreachable | |
|---|---|---|---|
| `root` only | 3,707 | 5,879 | WRONG — loses all test reachability |
| `root \|\| test \|\| bench` | 9,281 | 198 | WRONG — self-rooting hides dead helpers |
| **`root \|\| test_entry \|\| bench`** | **8,946** | **226** | what magma now computes |

Translated into what a user actually sees — dead rows (`unreachable && !generated`, matching
`contract.Node.IsDead`):

**correct seed 59 dead rows, broad seed 33. The broad seed would have hidden 26 genuinely dead
functions — 44% of the report.**

That is the concrete value of the `test_entry` field, on a real workspace, and it is why the gap
was worth closing before wiring rather than after. Note also that the correct seed sits OUTSIDE the
range bracketed by the two wrong ones for dead rows (59 > 33), so neither wrong option was a
conservative approximation of it in the direction that matters.

---

# POST-WIRING ADVERSARIAL REVIEW — 2026-07-31

Run over the wired state, three lenses (soundness / contract / instrument). It found three real
defects, which is the answer to whether it was still warranted after 16/16 green.

## Lens 1 (soundness) — FAMILY G: exported symbols are not roots

**A new false-dead-code family, reproduced against rustc before any fix.** `#[no_mangle]` and
`#[export_name]` make a function externally callable whether or not it is `pub`: the compiler emits
the symbol and a C / Python / WASM / interrupt-vector caller invokes it, so nothing inside the Rust
graph calls it — exactly as nothing calls `main`. rustc's `dead_code` lint treats such items, and
everything they reach, as live. `roots.rs` knew about a bin's `main` and about publicly reachable
items, and not about exported symbols.

On a five-line crate the helper reported **two functions dead that rustc reports live**. This is the
canonical shape of every Rust library exposed over FFI — a cdylib for C/Python/Node, a staticlib, a
WASM export, an embedded interrupt handler — so it is not a corner case.

**Second, quieter symptom:** a cdylib with NO `pub` API at all, an entirely reasonable FFI crate,
was **refused** outright ("no roots in scope") on the false premise that it had no entry points.

**Why the gate could not see it.** `oracle-diff.sh` excludes any function carrying these attributes
from the comparison, on the sound reasoning that rustc never reports them dead even when they are,
so the oracle cannot adjudicate them. True — and it also meant the helper's OWN answer for those
nodes was never checked, so the attributed function reading dead was structurally invisible. Only
the second hop, its callee, leaked through as a FATAL, and no fixture had a second hop. **That is
five exclusions in this harness now found to mask something.** An exclusion can be perfectly sound
as an oracle-comparison decision and still hide a user-facing defect.

**Not root-widening.** The standing rule warns against inventing roots to paper over MISSING EDGES
— the family B trap. Here there is no edge to find: the call originates outside the program. The
control is `truly_dead`, genuinely dead, agreed dead by rustc, and still reported after the fix
(`agree_dead: 1`). `considered` stays 4 and `excluded` stays 2 across the fix.

`testdata/ffi_export`, confirmed RED first (2 FATALs both configs). 16/16 fixtures now pass.

## Lens 2 (contract) — `executed_target_code` was parsed and thrown away

A trust-boundary fact — producing a Rust graph RUNS the repo's own code, where Go analysis only
type-checks — that the handover records as a SETTLED contract decision Architext had already
declared. The helper always emitted it; magma read it into a struct member nothing consumed,
because `contract.Graph` had no field for it. A consumer was left to infer it from
`language == "rust"`, which is wrong the moment a sandboxed mode exists — the exact reason the
helper derives it per run instead of hard-coding it. Now carried through to the Architext emitter.
Go leaves it `false`, which is the correct answer for a backend that never runs target code.

## Lens 3 (instrument) — Family F's refusal had no test

`relocate_out_of_root` repairs every shape currently known, so `main::non_local_paths` fires on no
fixture, and a regression breaking it would have been **silent** — precisely the failure mode the
check exists to prevent. Now driven directly by a unit test, which was mutation-checked (disabling
the predicate makes it fail) rather than merely observed to pass.

## Also found by the CI work, not by the review

**`cargo test` had been red for 26 commits.** Symbol qualification (`f20e513`) landed 42 commits
after `tests/multi_impl.rs` was last touched; the test looked up `speak` where the helper now emits
`<Cat as Speak>::speak`. Nothing ran `cargo test` — CI gated `gofmt`, `go vet` and the Go tests and
never built rust-helper at all. A test nothing runs is not a test.

## Identified but NOT investigated — no local code to test against

`#[panic_handler]`, `#[global_allocator]`, `#[start]` are the same "the compiler calls this" shape
as family G and plausibly share the defect. Recorded rather than fixed, because fixing them without
a fixture would be acting on plausibility — the thing this project's own rules forbid. A no_std
probe crate would settle it.

## Standing lesson this review adds

> **A workaround inside the measuring instrument can hide a hole in the thing being measured.**
> `test_entry` was invisible because `oracle-diff.sh` solved it for itself with a regex over source
> text. Family G's first hop was invisible because `oracle-diff.sh` excluded it. In both cases the
> harness had coped, and coping is what stopped anyone noticing the contract could not. **When the
> harness needs a workaround, ask what a second consumer would do without it.**

---

# ARCHITEXT SAMPLE — first real Rust emit, ready to send

`~/magma-samples/code-graph-roboticus-rust-7e5f0d6d.json` (15 MB), generated at magma `4695965`
against `roboticus-rust` at a **clean** tree. Alongside the two existing Go samples.

```
roboticus-rust   rust · 7e5f0d6d
nodes 10,921   edges 24,611   dead code 59   test-only 21
```

**Cross-validation worth noting:** 59 dead rows is exactly what an independent offline BFS over the
helper's own output predicted for the correct (`root || test_entry || bench`) seed, computed before
the backend existed. Two implementations written separately agreeing to the row is the same kind of
evidence the Go/Architext badge reconciliation gave.

Validated on every axis that has previously caught a defect:

| check | result |
|---|---|
| `contract_version` | `magma-code-graph/1` |
| `fidelity` | `semantic` (settled, one-sided — Architext's schema is `{"type":"string"}`) |
| `executed_target_code` | **`true`** — the field that was being parsed and dropped until this session |
| `tree` | `clean`, not dirty |
| absolute paths in `file` | **0** |
| any mention of the home directory | **0** |
| `column`/`bench`/`trait_impl`/`macro_truncated`/`test_entry` | **absent** — strip enforced by `contract.Node` having no such fields |

**The Rust artifact does NOT have the Go-side path leak.** That separate open defect (`--architext`
emitting build-cache absolute paths for Go build stubs, 201 nodes on roboticus, cross-machine
non-deterministic) has no counterpart here: zero absolute paths across 10,921 nodes. The Go task
still stands on its own.

**Not sent.** Transmitting this is an outward-facing action and is the user's to take. Everything
needed is above; the process of sending a real emit for validation before wiring is three-for-three
at catching defects pre-ship, so it is worth doing before release.

---

# ARCHITEXT VALIDATION ROUND — two defects, one contract decision (2026-08-01)

The sample was sent, validated, and **failed**. Both defects were mine; both are fixed.

## Defect 1 — `signature.params`/`results` emitted `null`, not `[]` (FIXED, `f3e1827`)

Architext's validator rejected the artifact outright. Measured across 10,921 functions: 7,442
null params (68%), 5,456 null results (50%), 2,326 with ONE null and one array — the last being the
tell that it was per-field, i.e. a nil Go slice, not a whole-object switch.

**`golang.go`'s `signatureOf` had always initialised both fields explicitly. I wrote the Rust
mapping fresh as `&contract.Signature{}` and appended, so a no-parameter function left a nil slice,
which Go marshals as `null`.** The invariant lived in one backend's code instead of in the type, so
the second backend did not inherit it.

Fixed in `contract.Signature.MarshalJSON`, which coerces nil to `[]` — structurally impossible for
any backend to get wrong now, rather than relying on the next author noticing what `signatureOf`
does. Mutation-checked. Re-emit converted the populations exactly: 7,442 / 5,456 / 5,286 null →
the identical counts as `[]`.

Architext declined to relax their schema to accept `null`, correctly: "unknown parameters" and "no
parameters" are different claims and only the second can be true of a function magma has analysed.

## Defect 2 — the re-emit silently did nothing

The first re-emit hit magma's freshness check and skipped. **Freshness is keyed on the ANALYSED
repo's sha, not on magma's own version** — `roboticus-rust` had not changed, though magma had, which
was the entire point. `--force` is required when regenerating a sample after a magma-side fix.
Caught only by validating the file rather than the run's exit code. Add `--force` to any
sample-regeneration step.

## Contract decision — `kind: "init"` added to the enum (two-sided, Architext landing it)

274 nodes (2.5%) carry `kind: "init"`, which violated their `{"enum":["func","method"]}`. This is
the governance rule earning its keep: `fidelity` is `{"type":"string"}` on their side so
`"semantic"` shipped one-sided, while `kind` is pinned — **same "new value on an existing field"
shape, opposite governance, decided entirely by how the CONSUMER constrains the field.** Neither
side could know without checking.

**Decided (b): add `"init"`, do not map to `"func"`.** The concrete reason, measured rather than
argued — the 22 init nodes landing in the dead badge are `static`s:

    ENV_MUTEX#init        test_support.rs:4
    TEST_NONCE#init       dashboard.rs:91
    MACHINE_ID_MUTEX#init keystore.rs:689

Mapping to `"func"` would render `ENV_MUTEX#init` as a dead FUNCTION, sending a reader to delete a
function that does not exist. That is an actionable wrong answer, 22 times.

**What the 274 actually buy, measured:** 4 edges, 1 distinct callee, and exactly **1** function
(`PatternSet::compile`, injection.rs:36) reachable ONLY via an init edge. One false dead-code row
prevented out of 10,921 nodes. Small, and still worth it — a false dead-code row is a deletion
order for live code. Option (c), dropping the nodes, would mechanically reintroduce it.

**Go/Rust asymmetry, deliberate — do not "fix" it.** Go's `init#N` stays `kind: "func"` because it
IS a real function (compiler-generated, has a body, called by the runtime). Rust's `#init` is a
magma-synthesized node wrapping an initialiser EXPRESSION; there is no `fn` in the source. Same
name, different things, correctly different kinds.

## Disclosed to Architext, not fixed: magma models calls, not uses

Those 22 land in `dead` rather than `test_only` because a `static` is *used*, never *called*, so no
edge ever points at its initialiser. A test-support static therefore has no incoming edge, fails
`reachable`, and drops into the dead badge despite `test: true`. A "uses" edge is a real design
question, not a patch — flagged rather than papered over, because Architext is about to render
these and the confidence they present them with should reflect it.

## Third independent badge reconciliation

Architext's predicates give dead=59 / test_only=21 against magma's 59 / 21 on the fixed Rust emit —
after Go, and after the pre-fix Rust emit. Different language backend, different analysis semantics,
same numbers.

## The standing lesson, now confirmed from both sides

> **When a component copes with a missing guarantee locally, the coping is what stops anyone
> noticing the contract cannot.** Go's backend coped by initialising the slices itself, which is why
> nothing noticed `contract.Signature` had no such guarantee. `oracle-diff.sh` coped with the
> missing `#[test]` signal via a source regex, and with `#[no_mangle]` via an exclusion, which is why
> neither gap was visible until a second consumer existed. Four instances, two on each side.

## CLOSED — first Rust artifact validates clean (2026-08-01)

    Architext validation passed.   (10,921 functions, 24,611 calls, 608 modules)

`kind` enum is now `["func","method","init"]` on their side (their commit `6a40d06`), RED confirmed
before with magma's exact error text, GREEN after, their full workspace clean. **`init` may be
emitted freely.** No further coordination outstanding on the Rust artifact.

**They put the Go/Rust asymmetry warning in the code, not in a ledger** — verbatim in the enum's own
test doc comment, on the reasoning that "a ledger is not where someone stands when they're about to
tidy up an enum." Worth copying as a habit: the warning belongs where the mistake would be made.

**The calls-vs-uses disclosure changed their rendering.** They recorded it as a rendering
constraint rather than trivia: a `static`'s initialiser can never receive an edge, so those nodes
carry *weaker evidence than a function in the same badge*, and their UI will not present them with
equal confidence. They explicitly distinguished it from their generic reflection/cgo caveat because
the blind spot has a **structural** cause rather than a heuristic one. Disclosing a weakness in our
own data is what let them render it honestly — and they did not ask us to build a "uses" edge.

**Five defects caught pre-ship by this handshake**, across both sides: `tree` carrying the SHA,
`executed_target_code` vs `additionalProperties:false`, their own `fidelity` minLength,
`signature.*: null`, and `kind: "init"` — the last a contract gap rather than a bug, surfaced only
because fixing the previous one unmasked it.

**Their sharper formulation of the standing lesson, which supersedes mine:**

> **Local competence is what makes a missing guarantee invisible — the healthier the component, the
> longer nobody looks.**

Every one of the four cases fits it: `golang.go` initialising its own slices, `oracle-diff.sh`'s
`#[test]` regex and `#[no_mangle]` exclusion, and their own validator passing a document whose
`fidelity` was nonsense. In each, the coping component was working perfectly.

---

# SECOND REAL RUST PROJECT — a Bevy game (2026-08-01)

Run against `dark-tower`, a Bevy 0.12 game, as a second real-world sample beyond `roboticus-rust`.
It found a defect immediately, which is the argument for having done it.

**Note it is not a git repo**, so it had to be snapshotted (APFS clone + `git init`) to analyse.
magma's refusal for that case is clean and names the likely cause:
`reading git metadata (is "…" a git repo?): exit status 128`.

## Defect found: a symlinked root silently defeats the target-dir exclusion (FIXED, `740162d`)

magma REFUSED over type errors in `target/debug/build/clang-sys-*/out/*.rs` — generated build-script
output it should never have scanned. `cargo metadata` reports `target_directory` canonicalised while
rust-analyzer's vfs reports paths as given; macOS resolves `/tmp` → `/private/tmp`, so the prefix
comparison never matched.

**The refusal was the mild symptom.** `enumerate.rs` anchors the `generated` flag on the same
`target_dir` — the H1 fix — so under a symlinked root it stops firing for exactly the build-script
output it exists to catch. `/tmp` and `/var` are symlinked on macOS and symlinked checkouts are
ordinary on CI; `roboticus-rust` missed it only because its path has no symlink.

Fixed by canonicalising once and feeding BOTH `cargo metadata` and `load_workspace_at`. Canonicalising
only the former is worse than the bug: vfs paths would stop stripping, every node would emit an
absolute path, and `non_local_paths` would refuse the entire run.

## Measured cost of the derive over-rooting residual — it is bigger than recorded

The map came out **156 nodes, 137 edges, 0 dead**:

    roots            136 / 156   (87%)
      generated       46         <- derive-relocated methods
    exported         123
    dead reported      0

`<GameState as Clone>::clone`, `::fmt`, `::default`, `::hash`, `::eq` — all `root:true,
exported:true`, because rust-analyzer reports a derive-generated method's own Visibility as Public.
This document already records that as "over-reports toward LIVE, safe direction, deliberately not
fixed", noted against a single fixture. **On a derive-heavy codebase it is 29% of all nodes rooted
for no real reason, and the map reports zero dead functions out of 156.** The analysis retains
almost no discriminating power there.

**Still NOT fixing it, and the precondition is worth restating** because `?` and `Drop` closing
makes it tempting: the recorded constraint is not only those two. Format-args and `for`-loop
resolution still cover only concrete-`Adt` types — `.to_string()`, `dyn Trait` format args, and
generics remain uncovered — and narrowing roots before those close would manufacture false dead
code. The measurement above raises the PRIORITY of closing them; it does not license tightening
roots early.

## Family A, working on a real Bevy project

    dynamic edges 48   static edges 89
    <StrategicPlugin as Plugin>::build -> setup_strategic_camera
    <GamePlugin as Plugin>::build     -> initialize_strategic_map

That is `app.add_systems(Update, setup_strategic_camera)` — the system passed as a VALUE, never
called syntactically. Bevy is built entirely this way, so without family A every system function in
the project would have had no incoming edge.

Honest sizing: only **4** functions would actually flip to unreachable without the dynamic edges,
because 87% of the crate is already a root (see above). The family is doing real work; the
over-rooting is masking how much.
