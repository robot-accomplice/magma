# Rust helper (Plan A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `rust-helper/` from spike code into a binary that emits a complete, correct JSON call graph for a Cargo workspace — everything magma's Go side needs to build a `contract.Graph`.

**Architecture:** The helper is a pure *extractor*. It loads a workspace through rust-analyzer, enumerates workspace-local functions with full metadata, walks each body (descending into macros) to resolve call targets, and emits one JSON document. It computes **no** reachability, fan, or views — those are derivation, and magma (Go) owns them. This split keeps graph-walking in one place and the helper independently testable.

**Tech Stack:** Rust 2021, `ra_ap_* = "=0.0.343"` (rust-analyzer as a library), `serde`/`serde_json`. Built in **release** mode.

**Spec:** `docs/superpowers/specs/2026-07-29-rust-backend-design.md`
**Evidence:** `docs/superpowers/research/2026-07-29-rust-backend-feasibility.md`
**Scope note:** This is Plan A of two. Plan B (Go-side backend, views, cross-check, consent gates, release pipeline) is written after this plan's JSON output is stable, because B consumes A's exact shape.

## Global Constraints

- **Build in release.** Measured 43.4s release vs 710.3s debug on a 575k-line workspace; the debug edge phase failed to complete at all on three attempts. Never benchmark or ship a debug build.
- **Wrap all `Semantics` work in `ra_ap_hir::attach_db(db, || …)`.** rust-analyzer's type inference requires the salsa DB attached to the calling thread; without it inference panics inside `hir_ty::next_solver::interner` with *"Try to use attached db, but not db is attached"*.
- **Filter to `CrateOrigin::Local`** (from `ra_ap_ide_db::base_db`). Without it enumeration returns 118,081 functions (whole dependency closure + std) instead of 8,437.
- **Enable `cfg(test)`** via `CargoConfig.cfg_overrides` = `CfgDiff::new(vec![CfgAtom::Flag(sym::test.clone())], Vec::new())`. Off by default, and without it no test code exists in the graph.
- **Never use `Analysis::outgoing_calls`.** It silently omits macro-generated edges → false dead code. Use `Semantics` body-walking.
- **Never use `CallHierarchyConfig { exclude_tests }`.** It filters the callee side only; verified inert for our purpose.
- **Dependencies pinned exactly** (`=0.0.343`). `ra_ap_*` has no API stability guarantee.
- **A library's `pub` API is a root** — rustc treats it so, and derived views must match the oracle. Do not port Go's no-`main` refusal.
- Helper writes **one JSON document to stdout**, diagnostics to **stderr**, non-zero exit with a machine-readable reason on refusal.

---

## File Structure

The spike is a single 154-line `main.rs`. It gets split by responsibility:

| file | responsibility |
|---|---|
| `src/main.rs` | CLI args, orchestration, exit codes. No analysis logic. |
| `src/load.rs` | Workspace loading: `CargoConfig`, `cfg(test)` overrides, `load_workspace_at`. |
| `src/model.rs` | The JSON DTOs (`Output`, `Function`, `Call`, `Signature`, `Refusal`) + serde. |
| `src/enumerate.rs` | Workspace-local function discovery and per-node metadata. |
| `src/walk.rs` | Body walking, macro descent, call resolution, edge dedup. |
| `src/roots.rs` | Root identification: bin `main`s + library `pub` API. |
| `testdata/fixture/` | Existing: Go `livemod` mirror + dyn/generic/macro/cfg(test). |
| `testdata/multi_impl/` | New (Task 6): two impls of one trait, dyn-dispatched. |
| `testdata/libonly/` | New (Task 5): library crate, no `main`. |

---

### Task 1: JSON output with stable IDs

The spike prints `EDGE\t<name>\t<name>`. Names are ambiguous — `Dog::speak` and `Cat::speak` both print as `speak`, which makes `dyn` dispatch impossible to validate. Everything downstream needs stable integer IDs.

**Files:**
- Create: `rust-helper/src/model.rs`
- Modify: `rust-helper/src/main.rs`
- Modify: `rust-helper/Cargo.toml` (add serde)

**Interfaces:**
- Produces: `Output { contract_version: String, functions: Vec<Function>, calls: Vec<Call> }`; `Function { id: u32, symbol: String }` (grown in Task 2); `Call { from: u32, to: u32 }` (grown in Task 4). `const CONTRACT_VERSION: &str = "magma-rust-helper/1"`.

- [ ] **Step 1: Add serde**

```bash
cd rust-helper && cargo add serde --features derive && cargo add serde_json
```

- [ ] **Step 2: Write `src/model.rs`**

```rust
//! The JSON contract between the helper and magma. The helper is an extractor:
//! it emits nodes and edges, never reachability or views — magma derives those.

use serde::Serialize;

/// Wire-format version of the helper's output. Bump on any breaking change.
pub const CONTRACT_VERSION: &str = "magma-rust-helper/1";

#[derive(Serialize)]
pub struct Output {
    pub contract_version: String,
    pub functions: Vec<Function>,
    pub calls: Vec<Call>,
}

impl Output {
    pub fn new(functions: Vec<Function>, calls: Vec<Call>) -> Self {
        Output { contract_version: CONTRACT_VERSION.to_owned(), functions, calls }
    }
}

/// One function or method. `id` is assigned by enumeration order and is the
/// ONLY way edges identify endpoints — names are ambiguous (two impls of one
/// trait share a method name).
#[derive(Serialize)]
pub struct Function {
    pub id: u32,
    pub symbol: String,
}

/// One call edge. Endpoints are Function ids.
#[derive(Serialize)]
pub struct Call {
    pub from: u32,
    pub to: u32,
}
```

- [ ] **Step 3: Emit JSON from `main.rs` instead of EDGE lines**

Replace the `println!("EDGE\t…")` calls. Collect into `Vec<Function>` / `Vec<Call>`, then at the end:

```rust
    let out = model::Output::new(functions, calls);
    println!("{}", serde_json::to_string_pretty(&out)?);
```

Add `mod model;` at the top of `main.rs`. Function ids are the index of the function in the enumeration vector (`u32`).

To resolve a call target to an *id*, build a lookup from `ra_ap_hir::Function` to id. `hir::Function` is `Copy + Eq + Hash`, so:

```rust
use std::collections::HashMap;
let index: HashMap<ra_ap_hir::Function, u32> =
    funcs.iter().enumerate().map(|(i, (_n, _p, f))| (*f, i as u32)).collect();
```

The walker returns `Vec<ra_ap_hir::Function>`; map each through `index`, skipping targets not in it (calls into dependencies are out of scope by the `CrateOrigin::Local` filter).

- [ ] **Step 4: Verify the output is valid JSON with distinct ids for same-named methods**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/fixture | jq '{v: .contract_version, fns: (.functions|length), calls: (.calls|length)}'
```
Expected: `contract_version` `"magma-rust-helper/1"`, 11 functions, **7** calls.

> **Why 7 and not 8** (corrected after Task 1 ran; the original estimate came from the
> pre-ID name-based spike output, which was counting an edge that does not survive
> id-resolution). The fixture's `t.speak()` goes through a `dyn Speak` receiver, and
> `sema.resolve_method_call` resolves it to the **trait's declared function**
> (`Speak::speak`), not to `Dog::speak`. Trait declarations are not enumerated — only impl
> methods and free functions are — so that target is absent from the id index and the edge
> is dropped. **This is a real false-dead-code bug**, now confirmed rather than suspected,
> and Task 6 fixes it. Do not "fix" it here by enumerating trait declarations: that would
> create edges to a declaration that has no body, which is not what the graph means.

```bash
./target/release/magma-rust-helper-spike testdata/fixture | jq -e '.calls | length == 7' && echo OK
```
Expected: `OK`.

- [ ] **Step 5: Commit**

```bash
git add rust-helper/src/model.rs rust-helper/src/main.rs rust-helper/Cargo.toml rust-helper/Cargo.lock
git commit -m "feat(rust-helper): emit JSON with stable function ids

Names are ambiguous — two impls of one trait share a method name, which makes
dyn dispatch impossible to validate. Edges now reference integer ids."
```

---

### Task 2: Node metadata

magma's `contract.Node` needs far more than a symbol. This task fills every field the helper is responsible for. It does NOT set `reachable`, `prod_reachable`, `fan_in`, `fan_out` — magma derives those.

**Files:**
- Modify: `rust-helper/src/model.rs`
- Create: `rust-helper/src/enumerate.rs`
- Modify: `rust-helper/src/main.rs`

**Interfaces:**
- Consumes: `Function` (Task 1).
- Produces: `Function` gains `pkg, file, line, kind, exported, test, root, generated`; `pub fn collect(db: &RootDatabase) -> Vec<(model::Function, ra_ap_hir::Function)>`.

- [ ] **Step 1: Extend the `Function` DTO**

In `src/model.rs`, replace the `Function` struct:

```rust
#[derive(Serialize)]
pub struct Function {
    pub id: u32,
    pub symbol: String,
    /// Crate + module path, e.g. "mycrate::net::client".
    pub pkg: String,
    /// Repo-relative path.
    pub file: String,
    pub line: u32,
    /// "func" for free functions AND associated fns without a receiver;
    /// "method" for anything with a `self` receiver.
    pub kind: String,
    /// `pub` visibility. Load-bearing: for a library this also determines root
    /// status, because rustc treats a lib's public API as the root set.
    pub exported: bool,
    /// Declared in test code (`Function::is_test`).
    pub test: bool,
    /// An entry point: a bin `main`, or a lib `pub` item. Set in Task 5.
    pub root: bool,
    /// Benchmark function (`Function::is_bench`) — counts toward all-roots only.
    pub bench: bool,
    /// Build-script- or macro-generated code.
    pub generated: bool,
}
```

- [ ] **Step 2: Write the failing check**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/fixture | jq -e '.functions[0] | has("kind") and has("test")'
```
Expected: FAIL (`jq` exits non-zero) — the fields do not exist yet.

- [ ] **Step 3: Write `src/enumerate.rs`**

```rust
//! Workspace-local function discovery with full node metadata.

use ra_ap_hir::{AssocItem, Crate, HasSource, ModuleDef};
use ra_ap_ide_db::base_db::CrateOrigin;
use ra_ap_ide_db::RootDatabase;
use ra_ap_syntax::ast::HasName;
use ra_ap_syntax::AstNode;

use crate::model;

/// Every function in workspace-member crates, paired with its hir handle.
///
/// The CrateOrigin::Local filter is not optional: without it this returns the
/// entire dependency closure plus std (118,081 vs 8,437 on roboticus-rust),
/// which floods the graph and produces false dead code.
pub fn collect(db: &RootDatabase) -> Vec<(model::Function, ra_ap_hir::Function)> {
    let mut out = Vec::new();
    for krate in Crate::all(db) {
        if !matches!(krate.origin(db), CrateOrigin::Local { .. }) {
            continue;
        }
        for module in krate.modules(db) {
            for decl in module.declarations(db) {
                if let ModuleDef::Function(f) = decl {
                    push(db, f, &mut out);
                }
            }
            for imp in module.impl_defs(db) {
                for item in imp.items(db) {
                    if let AssocItem::Function(f) = item {
                        push(db, f, &mut out);
                    }
                }
            }
        }
    }
    out
}

fn push(
    db: &RootDatabase,
    f: ra_ap_hir::Function,
    out: &mut Vec<(model::Function, ra_ap_hir::Function)>,
) {
    let Some(src) = f.source(db) else { return };
    let Some(name_node) = src.value.name() else { return };
    let Some(efid) = src.file_id.file_id() else { return };
    let file_id = efid.file_id(db);

    let line = db
        .line_index(file_id)
        .line_col(name_node.syntax().text_range().start())
        .line;

    let id = out.len() as u32;
    out.push((
        model::Function {
            id,
            symbol: f.name(db).as_str().to_owned(),
            pkg: module_path(db, f),
            file: db.file_path(file_id).to_string(),
            line: line + 1, // line_index is 0-based; magma reports 1-based
            kind: if f.self_param(db).is_some() { "method" } else { "func" }.to_owned(),
            exported: f.visibility(db) == ra_ap_hir::Visibility::Public,
            test: f.is_test(db),
            root: false,   // Task 5
            bench: f.is_bench(db),
            generated: false, // Task 7
        },
        f,
    ));
}

/// "crate::module::path" for the function's containing module.
fn module_path(db: &RootDatabase, f: ra_ap_hir::Function) -> String {
    let module = f.module(db);
    let krate = module.krate().display_name(db).map(|d| d.to_string()).unwrap_or_default();
    let mut parts: Vec<String> = module
        .path_to_root(db)
        .into_iter()
        .rev()
        .filter_map(|m| m.name(db).map(|n| n.as_str().to_owned()))
        .collect();
    parts.insert(0, krate);
    parts.join("::")
}
```

Add `mod enumerate;` to `main.rs` and replace the inline enumeration loop with `enumerate::collect(db)`.

> **Note on API drift:** exact method names (`self_param`, `visibility`, `line_index`, `file_path`, `path_to_root`) are from `ra_ap_hir`/`ra_ap_ide_db` 0.0.343. If any does not compile, find the equivalent by grepping the vendored source at `~/.cargo/registry/src/*/ra_ap_hir-0.0.343/src/lib.rs` — do **not** guess or drop the field.

- [ ] **Step 4: Verify metadata is populated and correct**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/fixture | jq '.functions[] | select(.symbol=="speak") | {symbol,kind,pkg,exported,test}'
```
Expected: `kind` is `"method"` (it has `&self`), `pkg` ends in `fixture`, `test` is `false`.

```bash
./target/release/magma-rust-helper-spike testdata/fixture | jq '.functions[] | select(.symbol=="t") | .test'
```
Expected: `true` — `t` is the `#[test]` function.

```bash
./target/release/magma-rust-helper-spike testdata/fixture | jq '[.functions[] | select(.kind=="method")] | length'
```
Expected: `1` (only `speak`).

- [ ] **Step 5: Commit**

```bash
git add rust-helper/src/enumerate.rs rust-helper/src/model.rs rust-helper/src/main.rs
git commit -m "feat(rust-helper): full node metadata (pkg, file, line, kind, exported, test, bench)"
```

---

### Task 3: Signatures and doc comments

**Files:**
- Modify: `rust-helper/src/model.rs`, `rust-helper/src/enumerate.rs`

**Interfaces:**
- Produces: `Function` gains `signature: Signature` and `doc: Option<String>`; `Signature { params: Vec<Param>, results: Vec<Result_> }`, `Param { name: Option<String>, ty: String }`, `Result_ { ty: String }`.

- [ ] **Step 1: Add the DTOs to `src/model.rs`**

```rust
#[derive(Serialize)]
pub struct Signature {
    pub params: Vec<Param>,
    pub results: Vec<Result_>,
}

#[derive(Serialize)]
pub struct Param {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    pub ty: String,
}

#[derive(Serialize)]
pub struct Result_ {
    pub ty: String,
}
```

Add to `Function`:

```rust
    pub signature: Signature,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub doc: Option<String>,
```

- [ ] **Step 2: Write the failing check**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/fixture | jq -e '.functions[] | select(.symbol=="gen_call") | .signature.params | length == 1'
```
Expected: FAIL — no `signature` field yet.

- [ ] **Step 3: Extract signature and doc in `enumerate.rs`**

Add to the `push` function, before constructing `model::Function`:

```rust
    let sig = f.ty(db);
    let params: Vec<model::Param> = f
        .params_without_self(db)
        .into_iter()
        .map(|p| model::Param {
            name: p.name(db).map(|n| n.as_str().to_owned()),
            ty: p.ty().display(db, ra_ap_hir::DisplayTarget::from_crate(db, f.krate(db).into())).to_string(),
        })
        .collect();
    let ret = f.ret_type(db);
    let ret_str = ret
        .display(db, ra_ap_hir::DisplayTarget::from_crate(db, f.krate(db).into()))
        .to_string();
    // Rust always has exactly one return type; "()" means no meaningful result,
    // which magma represents as an empty results list (matching Go's no-return).
    let results = if ret_str == "()" {
        Vec::new()
    } else {
        vec![model::Result_ { ty: ret_str }]
    };
    let doc = f.docs(db).map(|d| first_sentence(&d.into()));
    let _ = sig;
```

And add the helper:

```rust
/// First sentence of a doc comment — a pointer, not a payload. Mirrors Go's
/// use of doc.Synopsis.
fn first_sentence(text: &str) -> String {
    let trimmed = text.trim();
    match trimmed.find(". ") {
        Some(i) => trimmed[..=i].to_owned(),
        None => trimmed.lines().next().unwrap_or("").trim_end_matches('.').to_owned() + ".",
    }
}
```

Then set `signature: model::Signature { params, results }` and `doc` in the struct literal.

> **API drift note:** if `params_without_self` / `ret_type` / `docs` / `DisplayTarget` do not match 0.0.343, grep the vendored source rather than guessing. The *requirement* is: parameter names where present, type strings rendered module-relative, and the doc comment's first sentence.

- [ ] **Step 4: Verify**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/fixture | jq '.functions[] | select(.symbol=="gen_call") | .signature'
```
Expected: one param, and a `results` array that is empty (`gen_call` returns `()`).

- [ ] **Step 5: Commit**

```bash
git add rust-helper/src/model.rs rust-helper/src/enumerate.rs
git commit -m "feat(rust-helper): extract signatures (params + result types) and doc synopsis"
```

---

### Task 4: Edge metadata and deduplication

The spike pushes every resolved target with no dedup — 49,316 raw targets on roboticus-rust. Go's `collectEdges` aggregates per `(from, to)` and upgrades `dynamic` → `static` when any call site is static. The helper must match.

**Files:**
- Create: `rust-helper/src/walk.rs` (moved from `main.rs`)
- Modify: `rust-helper/src/model.rs`, `rust-helper/src/main.rs`

**Interfaces:**
- Consumes: `Call` (Task 1).
- Produces: `Call` gains `site_file, site_line, kind`; `pub fn edges(sema, db, funcs, index) -> Vec<model::Call>` returning **deduplicated** edges.

- [ ] **Step 1: Extend the `Call` DTO**

```rust
#[derive(Serialize)]
pub struct Call {
    pub from: u32,
    pub to: u32,
    pub site_file: String,
    pub site_line: u32,
    /// "static" when the target is resolved unambiguously; "dynamic" when the
    /// call goes through `dyn Trait` and the concrete impl is chosen at runtime.
    pub kind: String,
}
```

- [ ] **Step 2: Write the failing check for dedup**

The fixture's `main` calls `sq`-style helpers once each, so add a duplicate call to exercise dedup. Append to `testdata/fixture/src/main.rs` inside `fn live()`:

```rust
fn live() { helper(); helper(); }   // called TWICE — must dedup to ONE edge
```

Then:

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/fixture | jq -e '[.calls[] | select(.to == (.functions // [] | length))] | true' >/dev/null
./target/release/magma-rust-helper-spike testdata/fixture | jq '[.calls[] | "\(.from)->\(.to)"] | length as $total | ([.calls[] | "\(.from)->\(.to)"] | unique | length) as $uniq | {total: $total, uniq: $uniq}'
```
Expected: FAIL — `total` exceeds `uniq`, proving duplicates are emitted.

- [ ] **Step 3: Move the walker to `src/walk.rs` with dedup and edge metadata**

```rust
//! Body walking with macro descent. Never use Analysis::outgoing_calls: it
//! silently omits macro-generated edges, which produces false dead code.

use std::collections::HashMap;

use ra_ap_hir::{PathResolution, Semantics};
use ra_ap_ide_db::RootDatabase;
use ra_ap_syntax::{ast, AstNode, SyntaxNode};

use crate::model;

/// One resolved call site before aggregation.
struct Site {
    to: ra_ap_hir::Function,
    file: String,
    line: u32,
    dynamic: bool,
}

/// Deduplicated edges, aggregated per (from, to). A pair seen at both a static
/// and a dynamic site is recorded as "static" — matching Go, where any static
/// call site upgrades the pair.
pub fn edges(
    sema: &Semantics<'_, RootDatabase>,
    db: &RootDatabase,
    funcs: &[(model::Function, ra_ap_hir::Function)],
    index: &HashMap<ra_ap_hir::Function, u32>,
) -> Vec<model::Call> {
    let mut agg: HashMap<(u32, u32), model::Call> = HashMap::new();

    for (node, f) in funcs {
        let Some(src) = f.source(db) else { continue };
        if let Some(efid) = src.file_id.file_id() {
            let _ = sema.parse(efid);
        }
        let Some(body) = src.value.body() else { continue };

        let mut sites = Vec::new();
        walk(sema, body.syntax(), &mut sites, 0, db);

        for s in sites {
            let Some(&to) = index.get(&s.to) else { continue }; // out of workspace
            let key = (node.id, to);
            agg.entry(key)
                .and_modify(|c| {
                    if !s.dynamic {
                        c.kind = "static".to_owned(); // static site upgrades the pair
                    }
                })
                .or_insert(model::Call {
                    from: node.id,
                    to,
                    site_file: s.file.clone(),
                    site_line: s.line,
                    kind: if s.dynamic { "dynamic" } else { "static" }.to_owned(),
                });
        }
    }

    let mut out: Vec<model::Call> = agg.into_values().collect();
    out.sort_by_key(|c| (c.from, c.to)); // deterministic output
    out
}

fn walk(
    sema: &Semantics<'_, RootDatabase>,
    node: &SyntaxNode,
    out: &mut Vec<Site>,
    depth: usize,
    db: &RootDatabase,
) {
    if depth > 8 {
        return; // guard against pathological macro recursion
    }
    for n in node.descendants() {
        if let Some(mc) = ast::MacroCall::cast(n.clone()) {
            if let Some(exp) = sema.expand_macro_call(&mc) {
                walk(sema, &exp.value, out, depth + 1, db);
            }
        }
        if let Some(call) = ast::CallExpr::cast(n.clone()) {
            if let Some(ast::Expr::PathExpr(pe)) = call.expr() {
                if let Some(path) = pe.path() {
                    if let Some(PathResolution::Def(ra_ap_hir::ModuleDef::Function(f))) =
                        sema.resolve_path(&path)
                    {
                        out.push(site(sema, &n, f, false, db));
                    }
                }
            }
        }
        if let Some(mcall) = ast::MethodCallExpr::cast(n.clone()) {
            if let Some(f) = sema.resolve_method_call(&mcall) {
                // Task 6 refines dynamic detection for `dyn Trait` receivers.
                out.push(site(sema, &n, f, false, db));
            }
        }
    }
}

fn site(
    sema: &Semantics<'_, RootDatabase>,
    n: &SyntaxNode,
    to: ra_ap_hir::Function,
    dynamic: bool,
    db: &RootDatabase,
) -> Site {
    let range = sema.original_range(n);
    let file_id = range.file_id.file_id(db);
    let line = db.line_index(file_id).line_col(range.range.start()).line + 1;
    Site { to, file: db.file_path(file_id).to_string(), line, dynamic }
}
```

Wire `main.rs` to call `walk::edges(...)` inside the existing `attach_db` block, and delete the old inline walker.

- [ ] **Step 4: Verify dedup and metadata**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/fixture | jq '[.calls[] | "\(.from)->\(.to)"] | length as $t | ([.calls[] | "\(.from)->\(.to)"] | unique | length) as $u | {total:$t, uniq:$u}'
```
Expected: `total` equals `uniq` — `live` calling `helper()` twice yields exactly one edge.

```bash
./target/release/magma-rust-helper-spike testdata/fixture | jq '.calls[0] | {site_file, site_line, kind}'
```
Expected: a real file path, a positive line, and `kind` `"static"`.

- [ ] **Step 5: Commit**

```bash
git add rust-helper/src/walk.rs rust-helper/src/model.rs rust-helper/src/main.rs rust-helper/testdata/fixture/src/main.rs
git commit -m "feat(rust-helper): edge site metadata + dedup per (from,to)

Matches Go's collectEdges: one edge per caller/callee pair, and any static call
site upgrades the pair from dynamic to static."
```

---

### Task 5: Roots — bin `main`s and a library's `pub` API

Go refuses when there is no non-test `main`. **Rust must not do that** — most crates are libraries, and rustc treats a library's public API as live. Verified: on a lib crate with no `main`, `cargo check` reports only the private unreachable function, not the `pub fn` nor what it reaches.

**Files:**
- Create: `rust-helper/src/roots.rs`, `rust-helper/testdata/libonly/`
- Modify: `rust-helper/src/main.rs`

**Interfaces:**
- Consumes: `Function` (Task 2).
- Produces: `pub fn mark(db: &RootDatabase, funcs: &mut [(model::Function, ra_ap_hir::Function)])` setting `root`.

- [ ] **Step 1: Create the lib-only fixture**

`rust-helper/testdata/libonly/Cargo.toml`:

```toml
[package]
name = "libonly"
version = "0.1.0"
edition = "2021"
```

`rust-helper/testdata/libonly/src/lib.rs`:

```rust
//! A library crate with no `main` — the shape most Rust crates take.
pub fn public_api() { internal_helper(); }
fn internal_helper() {}
fn truly_dead() {}
```

Confirm the oracle's answer:

```bash
cd rust-helper/testdata/libonly && cargo check 2>&1 | grep "never used"
```
Expected: exactly one line, naming `truly_dead` — proving rustc treats `public_api` as a root.

- [ ] **Step 2: Write the failing check**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/libonly | jq '[.functions[] | select(.root)] | length'
```
Expected: FAIL — `0`, because nothing sets `root` yet. It must be `1` (`public_api`).

- [ ] **Step 3: Write `src/roots.rs`**

```rust
//! Root identification.
//!
//! Rust's root set is NOT Go's. Go refuses when a scope has no non-test `main`,
//! because a library cannot see its external callers. rustc answers differently:
//! it treats a library's `pub` API as live, and magma's derived views must match
//! the compiler set-identically. So a lib crate is analysable, and "dead" for a
//! library means "unreachable from the public API".

use ra_ap_ide_db::RootDatabase;

use crate::model;

/// Mark every production root: a bin target's `main`, or a `pub` item of a
/// library crate. Test and bench functions are NOT production roots — magma
/// walks them separately for the all-roots view.
pub fn mark(db: &RootDatabase, funcs: &mut [(model::Function, ra_ap_hir::Function)]) {
    for (node, f) in funcs.iter_mut() {
        let is_main = f.is_main(db);
        // A `pub` function in a library is externally callable, so it is a root.
        // Test functions are excluded: they are roots for the all-roots walk only.
        let is_public_api = node.exported && !node.test;
        node.root = is_main || is_public_api;
    }
}
```

Call `roots::mark(db, &mut funcs)` in `main.rs` after enumeration, before edge extraction. Add `mod roots;`.

- [ ] **Step 4: Verify against both fixtures**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/libonly | jq '[.functions[] | select(.root) | .symbol]'
```
Expected: `["public_api"]` — and NOT `internal_helper` or `truly_dead`.

```bash
./target/release/magma-rust-helper-spike testdata/fixture | jq '[.functions[] | select(.root) | .symbol]'
```
Expected: includes `main`.

- [ ] **Step 5: Commit**

```bash
git add rust-helper/src/roots.rs rust-helper/testdata/libonly rust-helper/src/main.rs
git commit -m "feat(rust-helper): roots = bin mains + library pub API

Deliberately NOT Go's no-main refusal. rustc treats a library's public API as
live, and derived views must match the oracle; refusing every lib crate would
also refuse most of the Rust ecosystem."
```

---

### Task 6: `dyn` dispatch — fix the confirmed false-dead-code bug

**No longer an open question — Task 1 answered it.** With stable ids in place, the behaviour is
observable and confirmed: `sema.resolve_method_call` on a `dyn Trait` receiver resolves to the
**trait's declared function**, not to any impl (verified via `AsAssocItem::container` →
`AssocItemContainer::Trait(_)`). Trait declarations are not enumerated, so the target is absent
from the id index and **the edge is dropped entirely**.

Consequence, and why this is Critical rather than cosmetic: a `dyn`-dispatched method has **no
incoming edge**, so every impl reachable only through `dyn` is reported unreachable — **false dead
code**, the exact failure magma exists to prevent, and the same class as the macro defect. The
oracle disagrees: `cargo check` on the multi-impl fixture reports nothing dead.

This task builds the fixture that makes the fix testable, then fixes it.

**Files:**
- Create: `rust-helper/testdata/multi_impl/`
- Modify: `rust-helper/src/walk.rs` (only if the investigation shows a gap)

**Interfaces:**
- Consumes: `walk::edges` (Task 4).

- [ ] **Step 1: Create the fixture**

`rust-helper/testdata/multi_impl/Cargo.toml`:

```toml
[package]
name = "multi_impl"
version = "0.1.0"
edition = "2021"
```

`rust-helper/testdata/multi_impl/src/main.rs`:

```rust
trait Speak { fn speak(&self); }
struct Dog;
struct Cat;
impl Speak for Dog { fn speak(&self) { dog_target(); } }
impl Speak for Cat { fn speak(&self) { cat_target(); } }
fn dog_target() {}
fn cat_target() {}
fn main() {
    let a: &dyn Speak = &Dog;
    a.speak();          // could be Dog::speak OR Cat::speak at runtime
    let _c = Cat;       // Cat is constructed, so rustc considers its impl live
}
```

- [ ] **Step 2: Record the oracle's answer**

```bash
cd rust-helper/testdata/multi_impl && cargo check 2>&1 | grep -c "never used"
```
Expected: `0` — rustc considers everything live, including `cat_target`.

- [ ] **Step 3: Determine what our graph says**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/multi_impl > /tmp/mi.json
jq '[.functions[] | {id, symbol, pkg}]' /tmp/mi.json
jq '[.calls[] | {from, to, kind}]' /tmp/mi.json
```

Now compute, by hand from the ids, whether **both** `speak` impls are reachable from `main`. The two `speak` functions have distinct ids (Task 1) — that is precisely why ids were required.

- [ ] **Step 4: Fix by over-approximating, mirroring Go's RTA**

Confirmed diagnosis (from Task 1): the call resolves to the trait's declared function, which is not
enumerated, so the edge vanishes. The fix is to **over-approximate deliberately** — the same choice
Go makes, and the safe direction: extra edges under-report dead code, missing edges invent it.

In `walk.rs`, when `resolve_method_call` returns a function whose container is a trait
(`AsAssocItem::container(db)` → `AssocItemContainer::Trait(t)`), do not emit an edge to the
declaration. Instead emit one `"dynamic"` edge to **every impl of that trait method in the
workspace**. Find the impl set via `ra_ap_hir` — grep the vendored source at
`~/.cargo/registry/src/*/ra_ap_hir-0.0.343/src/lib.rs` for an impls-for-trait query (candidates:
`Impl::all_for_trait`, `Impl::all_for_type`); confirm the real name before using it rather than
guessing.

Note this is precisely why edges carry `kind` (Task 4): these are genuinely dynamic dispatches, and
`"dynamic"` labels them as the over-approximation they are — matching Go's `fidelity: "rta"`
treatment of interface calls.

- [ ] **Step 5: Verify against the oracle**

```bash
cd rust-helper && cargo build --release
./target/release/magma-rust-helper-spike testdata/multi_impl > /tmp/mi.json
jq '[.calls[] | select(.kind=="dynamic")] | length' /tmp/mi.json
```
Expected: at least 2 — one edge per impl of `Speak::speak`.

Then confirm both impls are reachable from `main`, which is the property that matters:

```bash
jq -r '. as $g | [$g.functions[] | select(.symbol=="main") | .id][0] as $m
  | [$g.calls[] | select(.from==$m) | .to] as $direct
  | [$g.functions[] | select(.id as $i | any($direct[]; .==$i)) | .symbol]' /tmp/mi.json
```
Expected: includes **both** `speak` impls (two entries), not one.

Also re-check the original fixture now emits 8 calls (the dyn edge is restored):

```bash
./target/release/magma-rust-helper-spike testdata/fixture | jq '.calls | length'
```
Expected: `8`.

Add a regression test asserting `cat_target` is reachable from `main`, with a comment stating
**why**: rustc considers it live, so a graph that calls it dead diverges from the oracle and
reports false dead code.

- [ ] **Step 5: Commit**

```bash
git add rust-helper/testdata/multi_impl rust-helper/src/walk.rs
git commit -m "test(rust-helper): pin dyn-dispatch behaviour across multiple trait impls

The single-impl fixture could not distinguish 'resolved correctly' from
'resolved to one arbitrary impl'. Multiple impls make the difference observable,
and the oracle says every impl is live."
```

---

### Task 7: Generated-code detection and refusals

**Files:**
- Modify: `rust-helper/src/main.rs`, `rust-helper/src/model.rs`, `rust-helper/src/enumerate.rs`

**Interfaces:**
- Produces: `Refusal { contract_version, computable: false, reason: String }`; non-zero exit on refusal.

- [ ] **Step 1: Add the refusal DTO to `src/model.rs`**

```rust
/// Emitted instead of Output when analysis cannot proceed. magma turns this
/// into a refused graph — never a partial or degraded map.
#[derive(Serialize)]
pub struct Refusal {
    pub contract_version: String,
    pub computable: bool,
    pub reason: String,
}

impl Refusal {
    pub fn new(reason: impl Into<String>) -> Self {
        Refusal {
            contract_version: CONTRACT_VERSION.to_owned(),
            computable: false,
            reason: reason.into(),
        }
    }
}
```

- [ ] **Step 2: Mark generated code**

Build-script output lives under the target directory (`OUT_DIR`). In `enumerate.rs`'s `push`, set:

```rust
            generated: {
                let p = db.file_path(file_id).to_string();
                p.contains("/target/") || p.contains("/build/")
            },
```

This mirrors Go's exclusion of generated files, which removed 92 false test-only rows there.

- [ ] **Step 3: Refuse when there are no roots at all**

In `main.rs`, after `roots::mark`:

```rust
    if !funcs.iter().any(|(n, _)| n.root) {
        let r = model::Refusal::new(
            "no roots in scope (no binary target and no public API); reachability not computable",
        );
        println!("{}", serde_json::to_string_pretty(&r)?);
        std::process::exit(2);
    }
```

- [ ] **Step 4: Verify the refusal fires and exits non-zero**

```bash
cd /tmp && rm -rf norootfx && mkdir -p norootfx/src && cd norootfx
printf '[package]\nname = "norootfx"\nversion = "0.1.0"\nedition = "2021"\n' > Cargo.toml
printf 'fn only_private() {}\n' > src/lib.rs
cd ~/code/magma/rust-helper && ./target/release/magma-rust-helper-spike /tmp/norootfx | jq '{computable, reason}'; echo "exit=$?"
```
Expected: `computable: false`, the no-roots reason, and a non-zero exit.

```bash
./target/release/magma-rust-helper-spike testdata/libonly | jq -e '.computable // true' >/dev/null && echo "libonly still analysable"
```
Expected: `libonly still analysable` — a lib WITH a public API must not refuse.

- [ ] **Step 5: Commit**

```bash
git add rust-helper/src/model.rs rust-helper/src/main.rs rust-helper/src/enumerate.rs
git commit -m "feat(rust-helper): mark generated code; refuse honestly when no roots exist

Refusal is the degenerate case only (no bin target AND no public API), not Go's
lib rule — a library with a public API is analysable."
```

---

### Task 8: Oracle diff harness

Parity is a claim until proven. This task builds the tool that proves it, and runs it on real workspaces.

**Files:**
- Create: `rust-helper/scripts/oracle-diff.sh`

**Interfaces:**
- Consumes: the helper's JSON output (Tasks 1–7).

- [ ] **Step 1: Write the harness**

`rust-helper/scripts/oracle-diff.sh`:

```bash
#!/usr/bin/env bash
# Compare the helper's derived dead set against rustc's dead_code lint.
#
# Divergence directions are NOT equally severe:
#   helper says dead, oracle says live -> FALSE DEAD CODE (fatal)
#   helper says live, oracle says dead -> conservative (report only)
#
# Functions carrying liveness-affecting attributes are excluded from the
# comparison: the oracle deliberately says nothing about them.
set -euo pipefail
REPO="${1:?usage: oracle-diff.sh <workspace-root>}"
HELPER="$(cd "$(dirname "$0")/.." && pwd)/target/release/magma-rust-helper-spike"

"$HELPER" "$REPO" > /tmp/helper.json

# Derived dead set: reachable from no root, walking the edge set.
jq -r '
  . as $g
  | ([$g.functions[] | select(.root) | .id]) as $roots
  | reduce range(0; 50) as $_ ($roots;
      . + [ $g.calls[] | select(.from as $f | any(.[]; . == $f)) | .to ] | unique)
  | . as $reach
  | [$g.functions[] | select(.generated | not) | select(.id as $i | any($reach[]; . == $i) | not) | .symbol]
  | sort | unique | .[]
' /tmp/helper.json > /tmp/helper-dead.txt

( cd "$REPO" && cargo check --message-format=json 2>/dev/null ) \
  | jq -r 'select(.reason=="compiler-message")
           | .message | select(.code.code=="dead_code")
           | .spans[]? | select(.is_primary) | .text[]?.text' \
  | grep -oE "fn [a-zA-Z_][a-zA-Z0-9_]*" | sed 's/^fn //' | sort -u > /tmp/oracle-dead.txt

echo "=== FATAL: helper says dead, oracle says live (false dead code) ==="
comm -23 /tmp/helper-dead.txt /tmp/oracle-dead.txt || true
echo "=== report-only: helper says live, oracle says dead ==="
comm -13 /tmp/helper-dead.txt /tmp/oracle-dead.txt || true
```

```bash
chmod +x rust-helper/scripts/oracle-diff.sh
```

- [ ] **Step 2: Run on the fixtures**

```bash
cd ~/code/magma/rust-helper
./scripts/oracle-diff.sh testdata/fixture
./scripts/oracle-diff.sh testdata/libonly
./scripts/oracle-diff.sh testdata/multi_impl
```
Expected: the FATAL section is **empty** for all three. `libonly` should show `truly_dead` in both sets (agreement, so neither section lists it).

- [ ] **Step 3: Run on a real workspace**

```bash
cd ~/code/magma/rust-helper && ./scripts/oracle-diff.sh ~/code/roboticus-rust
```
Expected: FATAL section empty. If it is not, that is a genuine missed-edge bug — investigate before proceeding; do not proceed to Plan B with known false dead code.

- [ ] **Step 4: Record results in the research doc**

Append a "Plan A oracle results" section to `docs/superpowers/research/2026-07-29-rust-backend-feasibility.md` with the fixture and real-workspace outcomes, including counts for both divergence directions.

- [ ] **Step 5: Commit**

```bash
git add rust-helper/scripts/oracle-diff.sh docs/superpowers/research/2026-07-29-rust-backend-feasibility.md
git commit -m "test(rust-helper): oracle diff harness + recorded parity results"
```

---

## Self-Review

**1. Spec coverage.** Against `2026-07-29-rust-backend-design.md`, the helper's share:

| spec requirement | task |
|---|---|
| Workspace-local filter (req 1) | 2 (`CrateOrigin::Local` in `enumerate::collect`) |
| Prod/test flags (req 2, 3) | 2 (`is_test`, `is_bench`), 5 (`root`) |
| Generated-code exclusion (req 4) | 7 |
| Roots incl. lib `pub` API (req 5, §Roots) | 5 |
| `Semantics` walking + macro descent | 4 (moved from spike, preserved) |
| Edge dedup matching Go's `collectEdges` | 4 |
| Contract field mappings (`pkg`/`kind`/`exported`/`signature`…) | 2, 3 |
| Refusals (no roots) | 7 |
| Oracle validation | 8 |
| Release build, `attach_db`, pinned deps | Global Constraints |

**Deliberately out of scope for Plan A** (all Go-side, all Plan B): reachability/fan derivation, the cross-check policy and its attribute normalisation, consent gates, toolchain-install refusal, `fidelity: "semantic"`, release cross-compilation. The helper is an extractor; magma derives.

**2. Placeholder scan.** No "TBD"/"handle edge cases"/bare "write tests". Task 6 Step 4 is branching, not a placeholder: both branches specify concrete actions, and it branches because the answer is genuinely unknown until measured — the alternative would be inventing a result.

**3. Type consistency.** `model::Function` grows in Tasks 1→2→3 and is referenced by that name throughout; `model::Call` grows in 1→4. `enumerate::collect` returns `Vec<(model::Function, ra_ap_hir::Function)>`, which `roots::mark` mutates and `walk::edges` consumes — consistent across Tasks 2, 4, 5. `CONTRACT_VERSION` is defined once in Task 1 and reused by `Refusal` in Task 7.

**One known risk, stated rather than hidden:** exact `ra_ap_*` method names (`self_param`, `visibility`, `params_without_self`, `ret_type`, `docs`, `line_index`, `file_path`, `path_to_root`, `DisplayTarget`) are written from the 0.0.343 source but were not all compiled during planning. Tasks 2 and 3 carry an explicit instruction to grep the vendored source rather than guess or silently drop a field. This is the API-churn cost the operator accepted.
