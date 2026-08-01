# magma-rust-helper

The Rust backend for [magma](../README.md). Extracts a call graph from a Cargo
workspace using rust-analyzer as a library, and emits it as `magma-rust-helper/1`
JSON on stdout.

magma runs this for you — you should not normally need to invoke it directly.

## Install

```bash
cargo install --path .        # from this directory
```

magma finds it on `PATH`, or at `$MAGMA_RUST_HELPER`. Without it, a Rust repo is
refused with an install hint rather than analysed partially.

## Why it is a separate binary

It links rust-analyzer (`ra_ap_*`, pinned `=0.0.343`), so it cannot ship through
`go install` the way the rest of magma does. The version pin is load-bearing —
these crates make breaking changes across patch releases.

## Contract

- **stdout carries JSON and nothing else.** Progress and timings go to stderr,
  prefixed `PROGRESS ` and `TIMING `; magma surfaces the former as live progress.
- **An honest map or an honest refusal, never a partial one.** A workspace that
  does not type-check, has no roots in scope, or is not a Cargo project emits
  `{"computable": false, "reason": ...}` and exits 0 — a refusal is a real
  answer, not an error. Only usage misuse exits nonzero (2), matching Go's
  convention so magma can drive both backends identically.
- **Every imprecision fails toward "live".** Desugared calls (operators, `for`,
  `await`, format args, `?`'s error conversion, `Drop` at scope exit) resolve
  from types rather than syntax and are emitted as `dynamic` — real
  over-approximations, never invented edges. A node this map calls dead is dead
  conservatively.

## Correctness harness

The oracle gate compares every fixture against rustc's own `dead_code` lint in
two configurations, and fails on ANY divergence from a committed baseline — in
both directions, since a baselined divergence that vanishes usually means an
exclusion silently widened.

```bash
scripts/oracle-gate.sh testdata/collision
```

Run it for every fixture before changing anything here; CI does the same.
`testdata/*/oracle-expected.json` carries a written reason for each adjudicated
divergence.
