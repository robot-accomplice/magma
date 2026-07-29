# rust-helper — spike-verified starting point (NOT production)

This is the **verified spike code** from the Rust backend feasibility investigation, landed in the
repo so it is not lost again. It is the starting point for the helper described in
`docs/superpowers/specs/2026-07-29-rust-backend-design.md` — **not** the shipped helper.

## What it is

A Rust binary that drives rust-analyzer as a library to extract a call graph from a Cargo
workspace. Run it:

```sh
cargo build --release            # release is mandatory — see below
./target/release/magma-rust-helper-spike <workspace-root>            # Semantics walker (correct)
./target/release/magma-rust-helper-spike <workspace-root> --outgoing # outgoing_calls (defective)
```

Output: `EDGE\t<caller>\t<callee>` on stdout, `TIMING …` on stderr.

## What it proves

Run against `testdata/fixture` (which mirrors magma's Go `livemod` plus Rust's hard cases —
`dyn` dispatch, generics, `macro_rules!`, a `#[cfg(test)]` test):

- The `Semantics` walker finds **8 edges**; `--outgoing` finds **7**. The missing one is
  `main → macro_target` — a macro-generated call that `Analysis::outgoing_calls` silently drops,
  which would have produced **false dead code**.
- Derived reachability matches rustc's `dead_code` lint exactly: dead = `{dead}`,
  test-only = `{only_test}`.

Measured on roboticus-rust (575k lines): 48.4s total, 8,437 functions, 49,316 raw edges.

## Three things that cost real time to discover

1. **Build in release.** Debug measured 710s vs 48s — 16×, and the debug edge phase failed to
   complete at all on three attempts. Debug binary is 121 MB; release is 22 MB.
2. **`ra_ap_hir::attach_db(db, || …)` is mandatory** around all `Semantics` work. rust-analyzer's
   type inference needs the salsa DB attached to the calling thread; `Analysis::with_db` does it
   internally, `Semantics` does not, and inference panics deep in
   `hir_ty::next_solver::interner` with *"Try to use attached db, but not db is attached"*.
   Nothing in the `Semantics` API surface hints at this.
3. **Filter to `CrateOrigin::Local`.** Without it enumeration returns 118,081 functions
   (the whole dependency closure + std) instead of 8,437. This is the Rust analogue of the Go
   backend's `inModule` filter, and omitting it produces exactly the false-dead-code failure
   HANDOFF.md already records for Go.

## Known gaps before this is production

- **No deduplication.** Every resolved target is pushed; Go's `collectEdges` aggregates per
  `(from, to)` and upgrades `dynamic` → `static`. The 49,316 figure is therefore a raw count.
- **No JSON output.** Emits ad-hoc `EDGE` lines; the spec's contract is one JSON document.
- **No node metadata.** Does not yet emit `is_test` / `is_main` / `is_bench` flags, signatures,
  file/line, or the crate/module path — all required by the contract.
- **No refusals.** Missing toolchain and declined-consent paths are unimplemented.
- **Crate name is `magma-rust-helper-spike`** — rename when it becomes the real helper.

Dependencies are pinned exactly (`=0.0.343`); `ra_ap_*` offers no API stability guarantee, so
updates must be deliberate and reviewed.
