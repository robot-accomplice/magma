//! Body walking with macro descent. Never use Analysis::outgoing_calls: it
//! silently omits macro-generated edges, which produces false dead code.

use std::collections::HashMap;

use ra_ap_hir::{
    AsAssocItem, AssocItem, AssocItemContainer, HasSource, Impl, PathResolution, Semantics, Trait,
};
use ra_ap_ide_db::RootDatabase;
use ra_ap_paths::AbsPath;
use ra_ap_syntax::{ast, AstNode, SyntaxNode};
use ra_ap_vfs::Vfs;

use crate::enumerate;
use crate::enumerate::InitSource;
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
    vfs: &Vfs,
    root: &AbsPath,
    funcs: &[(model::Function, ra_ap_hir::Function)],
    index: &HashMap<ra_ap_hir::Function, u32>,
) -> Vec<model::Call> {
    let mut agg: HashMap<(u32, u32), model::Call> = HashMap::new();

    for (node, f) in funcs {
        let Some(src) = f.source(db) else { continue };
        // Cache this function's tree into `sema` before touching its body:
        // parse_or_expand handles a real file and a macro file uniformly, so
        // this also covers functions enumerate.rs now accepts whose own
        // definition comes from macro expansion (e.g. include!-generated
        // code, Task 11) — without this, resolving calls inside their body
        // below panics (Semantics::find_file: node not cached).
        let _ = sema.parse_or_expand(src.file_id);
        let Some(body) = src.value.body() else { continue };

        let mut sites = Vec::new();
        walk(sema, body.syntax(), &mut sites, 0, db, vfs, root);

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

/// Family C: closes the false-dead-code gap `edges()` above cannot reach —
/// a call written inside a const/static/associated-const initializer
/// expression, which is never a function's own body nor contained in one,
/// so `edges()`'s loop (which only ever visits `f.source(db).value.body()`
/// for an enumerated `ra_ap_hir::Function`) never sees it. `inits` pairs
/// each synthesized initializer node (`enumerate::collect_inits`) with the
/// hir handle needed to re-derive its initializer expression's syntax tree.
///
/// A new, additive entry point rather than a change to `edges()` or `walk()`
/// — both are untouched. The aggregation loop below is a deliberate,
/// near-verbatim duplicate of `edges()`'s: `walk()` (the actual traversal
/// this and `edges()` both feed into) is shared with a concurrently active
/// task and stays as the one and only place the traversal itself lives; only
/// the small "which bodies do I start from, and how do the ids materialise"
/// wrapper differs between a real function and a synthesized initializer.
pub fn init_edges(
    sema: &Semantics<'_, RootDatabase>,
    db: &RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
    inits: &[(model::Function, InitSource)],
    index: &HashMap<ra_ap_hir::Function, u32>,
) -> Vec<model::Call> {
    let mut agg: HashMap<(u32, u32), model::Call> = HashMap::new();

    for (node, src) in inits {
        let mut sites = Vec::new();
        match src {
            InitSource::Const(c) => {
                let Some(csrc) = c.source(db) else { continue };
                let _ = sema.parse_or_expand(csrc.file_id);
                let Some(body) = csrc.value.body() else { continue };
                walk(sema, body.syntax(), &mut sites, 0, db, vfs, root);
            }
            InitSource::Static(s) => {
                let Some(ssrc) = s.source(db) else { continue };
                let _ = sema.parse_or_expand(ssrc.file_id);
                let Some(body) = ssrc.value.body() else { continue };
                walk(sema, body.syntax(), &mut sites, 0, db, vfs, root);
            }
        }

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
    vfs: &Vfs,
    root: &AbsPath,
) {
    if depth > 8 {
        return; // guard against pathological macro recursion
    }
    for n in node.descendants() {
        if let Some(mc) = ast::MacroCall::cast(n.clone()) {
            if let Some(exp) = sema.expand_macro_call(&mc) {
                walk(sema, &exp.value, out, depth + 1, db, vfs, root);
            }
        }
        if let Some(call) = ast::CallExpr::cast(n.clone()) {
            if let Some(ast::Expr::PathExpr(pe)) = call.expr() {
                if let Some(path) = pe.path() {
                    if let Some(PathResolution::Def(ra_ap_hir::ModuleDef::Function(f))) =
                        sema.resolve_path(&path)
                    {
                        out.push(site(sema, &n, f, false, db, vfs, root));
                    }
                }
            }
        }
        // Family A: a function referenced as a VALUE (not called directly) —
        // `.map(double)`, a struct-field initializer (`Cfg { cb: cb_target }`),
        // a plain `let g = f;` binding, an array/table literal entry. Any of
        // these puts a bare PathExpr resolving to a function somewhere other
        // than a CallExpr's own callee slot; the callee slot is already
        // handled above (and must be excluded here, or it would double up).
        // Taking a function's address is not the same as calling it — we
        // cannot see whether/where the resulting value is ever invoked (that
        // would require real data-flow analysis, out of scope here) — so
        // this is marked `dynamic`, matching the existing `dyn Trait`
        // dispatch edges below: an approximation, not a proven direct call.
        // Mirrors Go's RTA treatment of address-taken functions. Emitting
        // this edge over-approximates liveness, which is the safe direction
        // (fails toward "live", never toward a false-dead deletion order);
        // omitting it is exactly the Family A false-dead-code defect.
        if let Some(pe) = ast::PathExpr::cast(n.clone()) {
            let is_call_callee = pe
                .syntax()
                .parent()
                .and_then(ast::CallExpr::cast)
                .and_then(|call| call.expr())
                .is_some_and(|callee| callee.syntax() == pe.syntax());
            if !is_call_callee {
                if let Some(path) = pe.path() {
                    if let Some(PathResolution::Def(ra_ap_hir::ModuleDef::Function(f))) =
                        sema.resolve_path(&path)
                    {
                        out.push(site(sema, &n, f, true, db, vfs, root));
                    }
                }
            }
        }
        if let Some(mcall) = ast::MethodCallExpr::cast(n.clone()) {
            if let Some(f) = sema.resolve_method_call(&mcall) {
                push_resolved(sema, &n, f, out, db, vfs, root);
            }
        }
        // Family B: desugared calls. Each of these expression forms invokes a
        // trait method without ever producing a syntactic CallExpr/
        // MethodCallExpr — `walk` above only ever looks for those two node
        // shapes, so e.g. `a + b` or `x.await` had NO edge at all, and the
        // trait impl they invoke read as dead. `Semantics` exposes real
        // resolution for each of these (confirmed against the pinned
        // ra_ap_hir=0.0.343 source, not name-matched), landing on the exact
        // impl `rustc` would pick for a concrete type, or on the trait's own
        // declared function when the receiver type is still generic at this
        // call site (mirrors the `dyn Trait` fallback above via the same
        // `push_resolved` helper). `a[i]` intentionally covers `Index` only:
        // `sema.resolve_index_expr` itself already folds in the `IndexMut`
        // case (see its doc comment upstream) when inference selected it, so
        // there is nothing left for this call site to special-case.
        if let Some(bin) = ast::BinExpr::cast(n.clone()) {
            if let Some(f) = sema.resolve_bin_expr(&bin) {
                push_resolved(sema, &n, f, out, db, vfs, root);
            }
        }
        if let Some(prefix) = ast::PrefixExpr::cast(n.clone()) {
            if let Some(f) = sema.resolve_prefix_expr(&prefix) {
                push_resolved(sema, &n, f, out, db, vfs, root);
            }
        }
        if let Some(index) = ast::IndexExpr::cast(n.clone()) {
            if let Some(f) = sema.resolve_index_expr(&index) {
                push_resolved(sema, &n, f, out, db, vfs, root);
            }
        }
        if let Some(await_expr) = ast::AwaitExpr::cast(n.clone()) {
            if let Some(f) = sema.resolve_await_to_poll(&await_expr) {
                push_resolved(sema, &n, f, out, db, vfs, root);
            }
        }
        if let Some(try_expr) = ast::TryExpr::cast(n.clone()) {
            // Resolves `Try::branch` — the first half of `?`'s desugaring.
            // For the two Try implementors stable Rust allows (`Result`,
            // `Option`), `branch` lives in core, so this edge is real but
            // never workspace-local (dropped by the `index` lookup in
            // `edges`/`init_edges`, same as any other out-of-workspace
            // target — harmless). What this does NOT resolve: the implicit
            // `From::from` error-type conversion `?` also performs via
            // `FromResidual` when the function's error type differs from the
            // propagated one — that conversion has no expression node of its
            // own for `Semantics` to resolve (it is a hidden argument to
            // `FromResidual::from_residual`, itself synthesized, with no
            // pinned-API equivalent of `resolve_await_to_poll` exposing it).
            // A user's `impl From<E1> for E2` reached only via `?` is
            // therefore still open — see the Task B report.
            if let Some(f) = sema.resolve_try_expr(&try_expr) {
                push_resolved(sema, &n, f, out, db, vfs, root);
            }
        }
    }
}

/// Given a function resolved via any dispatch path — a syntactic method
/// call, or one of the desugared-operator resolutions above — emit the right
/// edge or edges. If resolution landed on a trait's own declared function
/// (`AssocItemContainer::Trait`) rather than a concrete impl — the shape
/// both `dyn Trait` dispatch AND a still-generic desugared call site (e.g. a
/// `T: Add` bound with no concrete `T` at this call site) take — over-
/// approximate: emit a dynamic edge to the trait's own declaration node
/// (Task 9) AND to every impl of that method in the workspace, mirroring
/// Go's RTA. Extra edges under-report dead code (safe, the required
/// direction); missing edges invent it. A concrete resolution (inherent impl,
/// or a trait impl on a known concrete type) instead emits a single static
/// edge to the exact target.
fn push_resolved(
    sema: &Semantics<'_, RootDatabase>,
    n: &SyntaxNode,
    f: ra_ap_hir::Function,
    out: &mut Vec<Site>,
    db: &RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
) {
    match f.as_assoc_item(db).map(|assoc| assoc.container(db)) {
        Some(AssocItemContainer::Trait(t)) => {
            out.push(site(sema, n, f, true, db, vfs, root));
            let name = f.name(db);
            for imp in Impl::all_for_trait(db, t) {
                for item in imp.items(db) {
                    if let AssocItem::Function(impl_fn) = item {
                        if impl_fn.name(db) == name {
                            out.push(site(sema, n, impl_fn, true, db, vfs, root));
                        }
                    }
                }
            }
        }
        _ => out.push(site(sema, n, f, false, db, vfs, root)),
    }
}

fn site(
    sema: &Semantics<'_, RootDatabase>,
    n: &SyntaxNode,
    to: ra_ap_hir::Function,
    dynamic: bool,
    db: &RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
) -> Site {
    let range = sema.original_range(n);
    let file_id = range.file_id.file_id(db);
    // line_index is 0-based; magma reports 1-based (matches enumerate.rs).
    let line = ra_ap_ide_db::line_index(db, file_id).line_col(range.range.start()).line + 1;
    Site { to, file: enumerate::repo_relative_path(vfs, root, file_id), line, dynamic }
}
