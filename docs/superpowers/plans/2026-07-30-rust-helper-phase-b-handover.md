# Rust helper — Phase B handover

**Written 2026-07-30 at `df8cba4`, branch `feat/rust-helper-extraction` (46 commits ahead of
`develop`, tree clean, all tests green).**

Read this with `docs/superpowers/plans/2026-07-29-rust-helper.md` — specifically its final section,
"Plan A outcome: DO NOT SHIP", which is the authoritative finding list. This document is the
operating brief for the next session, not a duplicate of it.

---

## The one-paragraph version

Plan A (17 tasks) built a rust-analyzer-driven helper and a genuinely good oracle harness. The
harness is sound. The **extraction is not**: on a twelve-line idiomatic Rust program the helper
emits five functions and **zero** call edges, so magma would report four dead functions where
rustc reports one. Six independent false-dead-code families are confirmed and reproduced. The
branch's own gate is green only because no fixture exercises any of those shapes. Nothing consumes
the helper yet — Rust repos still refuse — so no user is exposed. **Do not merge as a working Rust
backend, do not wire it, and do not soften the "Rust is in development" language.**

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

- Every one is `generated: true` (12/12 and 201/201, zero exceptions), so filtering `generated`
  removes them completely.
- **Same-machine determinism is intact** — three `--force` runs byte-identical.
- **Cross-machine determinism is not**, because the path embeds a username and a platform-specific
  cache root. `--architext` writes into the repo's own `docs/architext/data/`, which is git-tracked
  in roboticus, so committing it would commit a developer's home directory path.

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
