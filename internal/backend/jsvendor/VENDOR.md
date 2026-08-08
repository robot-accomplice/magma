# Vendored TypeScript

    package    typescript
    version    5.9.3          EXACT — never a range, never `latest`
    sha512     sha512-jl1vZzPDinLr9eUt3J/t7V6FgNEw9QjvBPdysz9KfQDD41fQrC2Y4vKQdiaUpFT4bXlb1RHhLpp8wtm6M5TgSw==
    verified   2026-08-08, published integrity vs computed digest — exact match
    files      typescript.js (8.7 MB, the compiler), lib/*.d.ts (102 files, 3.8 MB)

## Why this exact version

`latest` is **7.0.2** — the Go-native rewrite, which ships as **platform-specific native binaries**
(`@typescript/typescript-darwin-arm64` and 17 more). Those are third-party analyzer binaries, which
locked decision #3 forbids outright. Pinning `typescript` without a version would have pulled that
tree on the next build.

5.9.3 is the newest 5.x, is a single pure-JS file, and has **zero** runtime dependencies — by a wide
margin the smallest supply-chain addition magma has made, against the 224 crates
`rust-helper/Cargo.lock` already carries.

## Why all 102 `lib/*.d.ts`

They are not optional. Without them the checker cannot resolve `Array`, `Promise` or `console`, and
every global goes unresolved. They are embedded **whole** rather than selected from the target's
`tsconfig` `lib`/`target`: selecting would make magma's own resolution floor depend on the analysed
repository's configuration, which is the silent variability the limitations design exists to prevent.

## Why not TypeScript 7 in-process

Investigated and blocked by the **Go compiler**, not by preference.
`github.com/microsoft/typescript-go` keeps its checker, binder and parser under `internal/` — 498
files there, only 8 under `cmd/` — and Go's `internal` rule is compiler-enforced, so no external
module can import any of it. Shelling out to its `tsgo` binary would violate decision #3; forking to
strip `internal/` means maintaining a fork of a 498-file compiler.

Re-checked 2026-08-08 against pseudo-version `v0.0.0-20260807224926-24fabe95acba`: **unchanged**.
License is Apache-2.0, so nothing blocks it but visibility.

If a public API ever lands, `jsEngine` in `internal/backend/jsengine.go` is the seam to swap at —
that is why the backend talks to an interface rather than to TypeScript directly. It would drop both
the `node` requirement and this vendored blob.

## Re-vendoring

```bash
cd /tmp && rm -rf tsv && mkdir tsv && cd tsv
npm pack typescript@<version>

# VERIFY BEFORE COPYING ANYTHING. These two must match exactly.
npm view typescript@<version> dist.integrity
echo "sha512-$(shasum -a 512 typescript-<version>.tgz | awk '{print $1}' | xxd -r -p | base64)"

tar xzf typescript-<version>.tgz
cd <magma>
cp /tmp/tsv/package/lib/typescript.js internal/backend/jsvendor/typescript.js
cp /tmp/tsv/package/lib/*.d.ts        internal/backend/jsvendor/lib/
ls internal/backend/jsvendor/lib/*.d.ts | wc -l   # expect 102
```

Update `Version` in `embed.go` and the version, hash and verification date above **in the same
commit** as the files. A vendored blob whose recorded provenance does not match its contents is
worse than no record at all.
