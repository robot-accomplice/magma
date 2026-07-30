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

---

### Task 9: Enumerate trait *declaration* methods (false dead code)

**Found during Task 5's fix loop; routed here because it is an enumeration defect, not a
root-flagging one.** `enumerate::collect` walks only `Module::declarations` (free functions) and
`Module::impl_defs` (impl methods). A trait's own declared methods are never enumerated, so a
default-bodied trait method is invisible — **and so are its outgoing calls**.

Reproduced against the oracle:

```rust
pub trait Tr { fn m(&self) { helper(); } }
fn helper() {}
```
`cargo check` emits **zero** warnings — rustc treats `helper` as live via `Tr::m`'s default body.
The helper emits `helper` with no incoming edge and **zero** calls: `Tr::m` was never enumerated,
so the `Tr::m → helper` edge does not exist. Direction: **false dead code**.

**Files:** `rust-helper/src/enumerate.rs`; new fixture `rust-helper/testdata/traitdecl/`.

Enumerate trait-declaration methods alongside impl methods. Grep the vendored source for the
trait's associated-items query (candidate: `Trait::items(db)` → `AssocItem::Function`) before
using it. Two things must both hold: the method appears as a node, and the body walker reaches its
default body so the outgoing edge is emitted.

Verification: the fixture above must yield an edge `Tr::m → helper`, and `helper` must not be
reported dead. Also confirm a trait method with **no** default body (`fn m(&self);`) is handled
sensibly — it has no body, so it emits no edges, and marking it a node with zero outgoing edges is
correct rather than a bug.

### Task 10: Trait impls on non-`Adt` self types (false dead code)

**Found during Task 5's fix loop, outside that task's required matrix.** `is_public_method`
requires `imp.self_ty(db).as_adt()` to succeed, which returns `None` for builtins, tuples, and
references. The `all_ancestors_public` fallback then requires `f.visibility(db) == Visibility::Public`,
which — per Task 5's root-cause finding — a trait-impl method can **never** satisfy.

Reproduced against the oracle:

```rust
pub trait Tr2 { fn m2(&self); }
impl Tr2 for i32 { fn m2(&self) {} }
```
`cargo check` emits zero warnings (a public trait's impl is public API regardless of self-type
shape); the helper reports `m2` → `root: false`. Direction: **false dead code**.

**Files:** `rust-helper/src/roots.rs`; extend `rust-helper/testdata/libonly/`.

For a **trait** impl, the self type's shape should not gate root status at all — if the trait is
publicly reachable and the impl exists in this workspace, the method is public API. Note the
orphan rule means a non-`Adt` self type can only appear in a trait impl, so this is precisely the
case where the `as_adt()` requirement is wrong.

Verification: `impl Tr2 for i32` → `m2` **root: true** (oracle silent); a private trait's impl on
`i32` → **root: false** (oracle warns); and Task 5's full 7-case matrix must still pass unchanged.

### Task 11: Enumerate `include!`-generated code (false dead code)

**Found during Task 7's review; pre-existing, reproduced at base commit `2ad40ed` as well.**
`enumerate::push` drops any function whose source resolves through macro expansion:

```rust
let Some(efid) = src.file_id.file_id() else { return };
```

`include!` is handled internally as a macro call, so `include!(concat!(env!("OUT_DIR"), "/generated.rs"))`
— **the standard build-script codegen pattern** — never produces a node. `walk::edges` iterates only
enumerated functions, so the generated function's outgoing calls are never walked either.

Reproduced against the oracle: a `build.rs` emitting `pub fn generated_fn() { production_callee(); }`,
`include!`d, with `main()` calling it. `cargo check` is **clean** — rustc sees
`main → generated_fn → production_callee`. The helper enumerates 3 functions, emits **zero** edges,
and leaves `production_callee` with no incoming edge. Direction: **false dead code**.

This matters more than a rare edge case: `#[path = ...]` cannot take `concat!(env!(...))`, so
`include!` is effectively the only mechanism for `OUT_DIR` codegen. magma executes build scripts
*specifically* to make generated code visible (§Security in the spec) — this gate discards it.

**Files:** `rust-helper/src/enumerate.rs`, `rust-helper/src/walk.rs`; new fixture
`rust-helper/testdata/buildgen/` with a real `build.rs`.

Teach enumeration to accept macro-expanded function definitions as real nodes and walk their
bodies. Constraints that must not break: macro **descent** in `walk.rs` stays intact (Task 4/6),
`outgoing_calls` never becomes the default, and the `generated` flag (Task 7) must be `true` for
these nodes so magma's views can exclude them the way Go excludes cgo output.

Verification: the fixture must emit an edge `generated_fn → production_callee`, `production_callee`
must not be reported dead, and the generated node must carry `generated: true`. `testdata/fixture`
(11 fns / 8 calls), `multi_impl`, and `libonly` must all be unchanged.

### Task 12: Resolve calls nested in macro *arguments* (false dead code)

**Found during Task 8's re-review; pre-existing, and out of that task's harness-only scope.**
The walker descends into macro *expansions* (Task 4/6), but a call written as a macro **argument**
yields no edge:

```rust
println!("{}", foo());   // -> ZERO edges to foo
```

Confirmed with a trivial repro independent of any test-module context. Moving the call to its own
statement before the `println!` resolves normally, which isolates the gap to argument position.

Direction: **false dead code** — `foo` gets no incoming edge and is reported dead while rustc
considers it live. This matters because calls inside macro arguments are pervasive: `println!`,
`format!`, `assert_eq!`, `vec!`, `write!`, and every logging macro.

**Files:** `rust-helper/src/walk.rs`; new fixture `rust-helper/testdata/macroarg/`.

The unexpanded AST holds the macro's arguments as a token tree rather than parsed expressions, so
`ast::CallExpr::cast` never matches them. Investigate whether descending into the expansion
surfaces the argument call (it should — the expansion contains the call), and if the current
descent is missing it, why: candidate causes are `expand_macro_call` returning a node whose
argument sub-expressions do not map back through `Semantics` resolution, or the resolution being
attempted against the unexpanded token rather than the expanded one. Grep the vendored source for
`descend_into_macros*` variants — one of them may be the intended tool for exactly this mapping.

Verification: the fixture must emit an edge to `foo` from a function containing
`println!("{}", foo())`, and `foo` must not be reported dead. Existing fixtures unchanged
(`fixture` 11 fns / 8 calls, `multi_impl`, `libonly`), and the oracle harness must stay FATAL=0.

### Task 13: Make the harness's cascade exclusion collision-proof

**Found during Task 9's re-review; pre-existing, and a defect in the measuring instrument
itself.** The shipped impl-cascade exclusion keys on the **bare trait/type name** captured from
rustc's diagnostic text (`oracle-diff.sh:158,163`). Names are not unique within a crate.

Demonstrated with a probe containing both a dead `Amb` and a live `Amb`:

```
line 2: trait `Amb` is never used          <- dead_side::Amb
line 9: method `never_used` is never used  <- live_side::Amb (rustc spoke per-method)
```

A name-keyed gate treats `dead_traits = ["Amb"]` as covering **`live_side::Amb`'s methods too** —
including ones rustc considers live. That is the same divergence-hiding slot Task 9's reverted
exclusion occupied; the shipped impl version is only narrowed by requiring trait *and* type to
collide together, which makes it rarer, not sound.

**The sound gate:** the `(file, line)` of the enclosing trait/impl declaration, matched against
the primary span of the `... is never used` diagnostic. rustc supplies both, and they cannot
collide.

**Cheapest correct route — and it deletes code rather than adding it.** The harness currently
cannot build that key because `helper.json` carries only `kind: "func"|"method"` with no
container. Have `enumerate.rs` emit the enclosing container's `(file, line)` as a field: it
already holds `db` and the `ra_ap_hir::Function`, and `as_assoc_item(db).container(db)` plus
`HasSource` yields it directly. With that field present the harness can drop its source-scanning
brace matcher entirely — removing both the brace-matching hazard and its O(methods × filesize)
subprocess cost.

**Files:** `rust-helper/src/enumerate.rs`, `rust-helper/src/model.rs`,
`rust-helper/scripts/oracle-diff.sh`, `rust-helper/scripts/oracle-diff.jq`.

Verification: the dead-`Amb`/live-`Amb` probe must NOT exclude `live_side::Amb`'s methods; the
existing cascade behaviour on `libonly` must be unchanged; all fixtures keep their current
FATAL counts (`libonly`'s one honest FATAL may legitimately become excluded once the gate is
sound — if so, say which and why).

> **Scoping correction for Task 13, found during Task 10's review — read before starting.**
> The `(file, line)` keying above fixes **collision soundness** only. It does **not** close the
> gap Task 10 surfaced: a builtin self type such as `i32` never receives its own `dead_code`
> diagnostic, however precisely the enclosing trait is keyed, so the "trait AND self type both
> independently dead" test can never be satisfied for it. Closing that requires an **additional**
> criterion:
>
> *trait independently reported dead AND (self type independently reported dead **OR** the self
> type is not a locally-defined item eligible for its own diagnostic).*
>
> Without this, Task 13 will ship believing it closed a gap it only half-closed, and `libonly`'s
> two `priv_m` FATALs will remain. Two independent tasks (9 and 10) have now hit this mechanism
> from different angles, which is the argument for prioritising it.

> **Task 12 — CLOSED by Task 11, and its premise was wrong.** Verified by A/B reproduction at
> Task 11's HEAD: `println!("{}", foo())` now emits `main → foo`; with Task 11's
> `sysroot = Some(RustLibSource::Discover)` disabled and rebuilt, it emits zero edges —
> exactly the defect this task was filed for.
>
> **Argument position was never the cause.** A call passed to a *local* `macro_rules!` macro
> resolved correctly even with the sysroot disabled. The real mechanism is that `println!`,
> `format!`, `write!`, `vec!`, `assert!`, `matches!` and friends are `macro_rules!` items defined
> in std/core, and `Semantics::expand_macro_call` cannot expand a macro it cannot resolve. So
> **any** call reachable only through a std macro — in any position — was silently invisible,
> despite the macro-descent logic from Tasks 4 and 6 being present and correct.
>
> **Consequence for this plan's earlier evidence:** every fixture count and the roboticus-rust
> real-workspace run (8,437 functions / 12,352 edges) predate the sysroot fix and were therefore
> taken under a helper blind to all std-macro-mediated calls. Treat them as stale and re-measure
> before citing them as parity evidence.

### Task 14: Reduce the sysroot load cost

Task 11's `sysroot = Some(RustLibSource::Discover)` fix is mandatory for correctness (+64% edges),
but it added **80.1s** to every run — up from 1.8s, a 44× increase and now the single largest
cost. Critically it is **fixed overhead**: independent of repo size, so it hurts small repos
proportionally worse than the 575k-line workspace it was measured on.

**Files:** `rust-helper/src/main.rs` (the `CargoConfig` construction), possibly `src/load.rs`.

Investigate, in rough order of likely payoff:

1. **Is it re-loaded per run when it need not be?** rust-analyzer caches sysroot metadata in its
   own workflows; check whether `RustLibSource::Discover` re-discovers and re-parses std/core/alloc
   every invocation, and whether a path-pinned variant (`RustLibSource::Path`) skips discovery.
2. **Is the whole sysroot needed?** magma only resolves *macros* from std/core — it never
   enumerates std functions (the `CrateOrigin::Local` filter discards them immediately). If
   sysroot loading can be limited to what macro resolution requires, most of the cost may be
   avoidable.
3. **Can it be cached across runs?** magma already has a freshness mechanism; a sysroot fingerprint
   (toolchain version + path) is stable across repos, so a cache would amortise across every
   invocation on a machine, not just re-runs of one repo.

Grep the vendored source for `RustLibSource`, `SysrootQueryMetadata`, and the sysroot loading path
in `ra_ap_project_model-0.0.343` before choosing an approach — confirm real names, do not guess.

**Verification:** correctness must not regress — roboticus-rust must still yield **10,651
functions / 20,268 edges**, and all fixtures keep their current counts and FATAL status. Report
the new load time and total against the 80.1s / 312.5s baseline. If the cost is irreducible, say
so with evidence; that is a legitimate outcome and better than a fragile optimisation.

### Task 15: Emit `executed_target_code`

One boolean, decided in the spec (§DECIDED). The helper is where the fact is known — it is the
process that runs build scripts and proc macros — so it emits it and magma's Go side passes it
through rather than re-deriving it.

**Files:** `rust-helper/src/model.rs`, `rust-helper/src/main.rs`.

Add `executed_target_code: bool` to `Output` (and to `Refusal`, where it is always `false` — a
refusal means nothing ran). For `Output` it is `true` whenever the workspace loaded with
`load_out_dirs_from_check: true` and the sysroot proc-macro server active, which is the only
configuration the helper currently uses — so it is `true` in practice, but derive it from the
actual `LoadCargoConfig` rather than hard-coding it, so it stays honest if a future sandboxed or
no-execution mode is added.

**Verification:** every fixture's JSON carries `"executed_target_code": true`; a refusal artifact
(private-items-only crate) carries `false`; no other field changes; all fixtures keep their
current function/edge counts and FATAL status.

### Task 16: Expected-FATAL baselines, so the harness can gate

**Proposed during Task 13's review; it answers a problem raised two tasks earlier and left open.**

Two fixtures (`libonly`, `collision`) now exit non-zero **by design**, carrying FATALs that are
honest and adjudicated. That is the correct outcome — suppressing them is the mistake this plan
has already had to undo twice — but it breaks the harness as a gate: a run that is red for
adjudicated reasons is indistinguishable from one red for a **new** reason, so the exit code stops
carrying information and gets ignored. No CI workflow invokes `oracle-diff.sh` today, and it
cannot be wired up while red is the normal state.

**The fix: make "known and adjudicated" machine-checkable instead of prose.**

- Commit a per-fixture baseline, e.g. `testdata/<crate>/oracle-expected.json`, listing each
  adjudicated FATAL by `symbol` + `file:line` with a one-line reason (`Task-9 trait-declaration
  gap`; `rustc attributes the unused-method diagnostic to the trait's method decl`).
- Have the harness — or a thin runner around it — diff the observed FATAL set against the baseline
  and exit **0 on exact match**, non-zero on **any** difference.
- **A disappearing FATAL must also fail.** That is the non-obvious half: a FATAL vanishing usually
  means an exclusion silently widened, which is exactly the failure mode this plan keeps hitting.
  An expected-set diff catches it; a threshold or a max-count would not.

Then `exit 0` means *"the instrument behaves as adjudicated"* rather than *"no FATALs"*, every red
run is real signal, and the fixtures become CI-able.

**Files:** `rust-helper/scripts/oracle-diff.sh` (or a new runner), `rust-helper/testdata/*/oracle-expected.json`, plus a CI workflow entry.

Verification: with baselines committed, all fixtures exit 0. Introduce a deliberate regression
(e.g. revert one line of a helper fix) and confirm it exits non-zero naming the new FATAL. Delete
a baseline entry the helper still produces and confirm that also fails. Both directions must fail
loudly, and adding a baseline entry must require a stated reason.

### Task 17: Column convention mismatch on non-ASCII source lines

**Found during Task 13's re-review. Real, reproducible, and fails safe — but it degrades the
measuring instrument's precision on any source line containing non-ASCII text.**

Task 13 keys the oracle match on `(file, line, column)`. The two sides disagree on what a column
is:

- **ra_ap / the helper** emits a **UTF-8 byte** offset (confirmed in `line-index-0.1.2/src/lib.rs`,
  documented as "Zero-based UTF-8 offset").
- **rustc** emits a **character** column.

Reproduction (`p_utf8b`): a dead function preceded on its own declaration line by a multi-byte
comment — `/* 日本語コメント */ fn dead_fn() {}` — gives rustc `col: 22` and the helper `column: 36`.
The keys never match, so the function surfaces as a **disclosed FATAL** rather than matching the
oracle's verdict.

**Why this is not urgent but is real:** byte offset is always ≥ character offset, so a mismatch can
only ever cause a spurious *non*-match — a missed agreement or a missed cascade exclusion. It can
**never** cause a spurious match, which is what would hide a divergence. The failure direction is
therefore safe: it produces visible FATALs, never silent exclusions. But every non-ASCII line costs
the instrument precision, and international codebases will hit it.

**Files:** `rust-helper/src/enumerate.rs` (column computation), possibly `rust-helper/scripts/oracle-diff.jq`.

Convert the byte offset to a character offset before emitting, or normalise both sides to a common
convention. Grep `line-index` and `ra_ap_ide_db`'s line-index API for a character-column accessor
before writing a conversion by hand.

Verification: `p_utf8b` must have the helper and rustc columns agree, and the function must match
the oracle instead of surfacing as a FATAL. All existing fixtures (ASCII-only) must be unchanged —
`collision` 3/1, `libonly` 2/3, and `fixture`/`multi_impl`/`traitdecl`/`buildgen` at 0.

---

# Plan A outcome: DO NOT SHIP. Extraction is unsound on ordinary Rust.

**Recorded 2026-07-30 at `755e0b1`, after the final whole-branch review (three reviewers,
three lenses). Tasks 1–17 are all complete and the branch's own oracle gate is GREEN — and that
green is the problem: no fixture exercises the shapes below, so the instrument never saw them.**

The helper does not meet the stated bar ("full parity or don't ship"). This is not a polish gap.
On a **twelve-line idiomatic program** the helper emits 5 functions and **zero** edges, so magma
would report 4 dead where rustc reports 1 — three false dead-code rows:

```rust
fn double(x: i32) -> i32 { x * 2 }          // passed to .map()      -> FALSE DEAD
fn handler_a() -> i32 { 1 }                  // in a static fn table  -> FALSE DEAD
const fn init_b_const() -> i32 { 9 }         // called from a static  -> FALSE DEAD
fn really_dead() -> i32 { 0 }                // genuinely dead        -> correct
static TABLE: [fn() -> i32; 1] = [handler_a];
static S: i32 = init_b_const();
fn main() { vec![1,2,3].into_iter().map(double).collect::<Vec<_>>(); }
```

A false dead-code row is the worst failure this tool has: it is a deletion order for live code.
**Containment is the only reason this is not an incident** — verified independently: only
`detect.Go` is registered (`internal/backend/backend.go`), nothing `exec`s the helper, and a real
Rust repo still refuses honestly. Nothing consumes any of this yet.

## Confirmed false-dead-code families (each reproduced against rustc, not argued)

| # | Family | Cause | Trigger frequency |
|---|---|---|---|
| A | Function used as a **value** — `.map(f)`, `[f]`, `let g = f;`, struct field | `walk.rs:94` casts only `CallExpr` with a `PathExpr` callee | Every higher-order call site |
| B | **Desugared** calls — `a + b`, `for`, `?`, `await`, `println!("{}", x)`, `Drop` | `walk.rs:88-141` models no desugaring, so operator/format/iterator trait impls get no incoming edge | Universal |
| C | Calls **outside a function body** — `const`/`static`/assoc-const initializers | `walk.rs:38-50` walks only `f.source(db).value.body()` | Common |
| D | **Macro depth-8 guard** silently truncates | `walk.rs:85` `if depth > 8 { return; }`, no counter, no disclosure | `serde_json::json!` exhausts it at the **2nd key**; `println!` alone costs 2 |
| E | **`cfg(test)` forced globally**, so `#[cfg(not(test))]` code is invisible and its callees read dead | `main.rs:31-34`; Go does **two** package loads precisely to avoid this | Any cfg-split codebase |
| F | Nodes emitted **inside the user's rustup toolchain** | `#[derive(Debug)]` expansion attributed to `core/src/fmt/mod.rs`; `generated:false` | `#[derive(Debug)]` is near-universal |

## Confirmed contract defects (independent of the above)

- **`executed_target_code` is false on its only reachable refusal path.** `load_workspace_at`
  (`main.rs:49`) runs build scripts and the proc-macro server; the refusal fires at `main.rs:77`
  and `Refusal::new` hard-codes `false`. The one field whose purpose is to be a trust boundary
  currently lies. *(Ledger correction: Task 15 recorded hard-coded-`false` as a safety property —
  "cannot accidentally carry true". That reading was wrong; it is simply incorrect.)*
- **Two refusal paths emit no machine-readable refusal at all** — `.expect` panics (exit 101),
  `load_workspace_at(..)?` exits 1. Go guarantees a `computable:false` envelope on every soft
  refusal. Exit-code convention is also inverted vs Go.
- **`test` is the `#[test]` attribute only**, so helpers in `#[cfg(test)] mod` and in `tests/`
  report `test:false`. Worse, `roots.rs:55-58` only exempts `node.test`, so a `#[cfg(test)] pub mod`
  becomes a **production root** — laundering test code into production and *hiding* real dead code.
  Go's analogue is the `_test.go` filename, which covers every helper in a test file.
- **`generated` is `is_macro_origin || path.contains("/target/") || path.contains("/build/")`** —
  an unanchored substring match. `src/build/mod.rs` and any hand-written `macro_rules!` function are
  marked generated, and `IsDead()` excludes generated, so this **silently suppresses dead rows**.
  *(Ledger correction: the deferral note claiming `/build/` "is a subset of `/target/`, adds no
  coverage" is false — `src/build/mod.rs` matches `/build/` only.)*
- **Edge `kind:"dynamic"` is CHA in Rust, RTA in Go.** `Impl::all_for_trait` emits an edge to every
  impl of a trait regardless of instantiation. Same field, same string, materially weaker analysis.
- **`symbol` is bare, and `(pkg, symbol)` is not unique** — `libonly` ids 14/15 are both
  `re_m`/`libonly::reexported_trait`. Go's `symbol` carries the receiver type (`T.Method`) precisely
  to avoid this; Architext slugs would collide into positional `-2`/`-3` suffixes.

## The trap to avoid when fixing family B

Trait impls of non-local traits (`Display`, `Iterator`, `Add`) survive today **only by accident**:
`roots.rs:184-196` marks them roots when the trait name happens to be `use`d into a publicly
reachable module. Root status therefore depends on where an unrelated import sits — verified:
moving `use std::fmt;` from the crate root into a private `error.rs` flips
`impl fmt::Display for MyErr` to false-dead.

The tempting fix — "treat impls of non-local traits as roots" — turns probe5 green while making
**every trait impl a permanent root**, so a dead trait impl could never be reported again, and it
leaves family B's real hole (no desugared edges) both open and untestable. **The fix must be at the
edge layer in `walk.rs`**: resolve `BinExpr`/`PrefixExpr`/`IndexExpr` → `ops::*`, `ForExpr` →
`IntoIterator::into_iter`/`Iterator::next`, `TryExpr` → `From::from`, `AwaitExpr` → `Future::poll`,
format args → `Display::fmt`/`Debug::fmt`, scope exit → `Drop::drop`. Only then can roots be
tightened to recover reporting power.

## Why the gate stayed green, and what that means for the harness

The harness is **sound and honest** — both reviewers who attacked it agree, and it caught every
one of these defects the moment it was given the right input. It refused rather than reporting a
false green on a warm cache. The failure is **fixture coverage, not instrumentation**: no
`testdata/` crate contains a function used as a value, a desugared operator call, a const
initializer, a deep macro, a `cfg(not(test))` item, or a `derive`. Verified: zero fixtures match.

**Therefore the first task of the next phase is fixtures, not fixes.** Add a crate per family
above, confirm each turns the gate RED with a FATAL that matches the family, and only then fix.
Otherwise the same green will be re-earned without the defect being gone. This is the pattern that
already burned this plan three times, at a larger scale.

## Sequencing

1. **Fixtures first** — one crate per family A–F; gate must go RED with the expected FATAL set.
2. Contract defects (cheap, independent): refusal honesty, refusal envelopes, `test`/`root`,
   `generated`, `symbol` uniqueness.
3. Family E (two loads) — matches Go's architecture; roughly doubles a run already at 130–270s
   on a large workspace. Measure before and after; do not guess.
4. Families A, C, F — mechanical once located.
5. Family B (desugaring) — the deepest, and the one that unblocks tightening roots.
6. Re-baseline `oracle-expected.json` per fixture only after each family is genuinely fixed.

**Do not wire a magma-side Rust backend, and do not change
`~/.claude/skills/magma/SKILL.md`'s "Rust is in development" line, until A–F are closed.** That
line is currently accurate and is the thing protecting users.
