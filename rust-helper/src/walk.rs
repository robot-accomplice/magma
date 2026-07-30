//! Body walking with macro descent. Never use Analysis::outgoing_calls: it
//! silently omits macro-generated edges, which produces false dead code.

use std::collections::HashMap;

use ra_ap_hir::{
    AsAssocItem, AssocItem, AssocItemContainer, Crate, HasSource, Impl, ModuleDef,
    PathResolution, ScopeDef, Semantics, Trait,
};
use ra_ap_ide_db::base_db::{CrateOrigin, LangCrateOrigin};
use ra_ap_ide_db::RootDatabase;
use ra_ap_paths::AbsPath;
use ra_ap_syntax::{ast, AstNode, SyntaxNode};
use ra_ap_vfs::Vfs;

use crate::enumerate;
use crate::enumerate::InitSource;
use crate::model;

/// Family D: how many nested macro-expansion boundaries `walk` will descend
/// through before giving up. Was a bare `8` with no evidence behind it and no
/// disclosure when hit — every callee past the guard silently vanished from
/// the graph, indistinguishable from a callee that was never there.
///
/// Measured (not guessed) against real macro shapes, via a throwaway probe
/// crate pinning actual `serde_json`, `tracing`, `tokio`, `bitflags`,
/// `lazy_static` from the local registry cache and reading the depth at
/// which each resolved a real function call inside its expansion:
///   - `println!("{}", f())` — depth 2 (`println!` -> `format_args_nl!`).
///   - `tokio::select! { _ = async { f() } => {} }` — depth 5.
///   - `tracing::info!(x = f(), "..")` — depth 6.
///   - `serde_json::json!({ "k1": f(), "k2": f(), ... })` — a tt-muncher that
///     costs ~3 levels of depth PER KEY: depths 6, 9, 12, 15, 18 for keys
///     1..5 in one run. This shape is open-ended by construction (each
///     additional key costs 3 more levels), so no finite limit makes it
///     unconditionally safe — see the disclosure below, which is why this
///     defect is fixed by disclosure first and a raised limit second, not
///     the limit alone.
///
/// 64 clears every measured real-macro shape above with headroom (a
/// `json!` object would need >20 keys to exhaust it — none of the fixtures
/// or workspaces this branch has touched come close), while remaining a
/// small, bounded number of syntax-tree recursion frames — cheap to walk
/// even when actually reached (see `Function::macro_truncated`'s doc comment
/// for what a consumer must do when it IS reached) — rather than removing
/// the guard, which the plan's own non-negotiables warn against doing
/// without justifying it against genuinely runaway/adversarial recursive
/// `macro_rules!` expansion. Never treat this constant as "the fix" on its
/// own: it only moves the truncation point further out. The Family D fix
/// checked in alongside it is what makes truncation observable when it
/// still happens, which a numeric limit — of any size — never can.
const MACRO_DEPTH_LIMIT: usize = 64;

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
pub fn edges<'db>(
    sema: &Semantics<'db, RootDatabase>,
    db: &'db RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
    funcs: &mut [(model::Function, ra_ap_hir::Function)],
    index: &HashMap<ra_ap_hir::Function, u32>,
) -> Vec<model::Call> {
    let mut agg: HashMap<(u32, u32), model::Call> = HashMap::new();

    for (node, f) in funcs.iter_mut() {
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
        // Family D: `truncated` is threaded through every recursive `walk`
        // call for this function's body and set once, deep inside, the
        // moment the guard fires — see `walk`'s own `depth >
        // MACRO_DEPTH_LIMIT` branch. Recorded directly on the node itself
        // (not just returned) so the disclosure survives into `functions`
        // without needing edges() to hand back a second, easy-to-drop
        // channel of its own.
        let mut truncated = false;
        walk(sema, body.syntax(), &mut sites, 0, db, vfs, root, &mut truncated);
        if truncated {
            node.macro_truncated = true;
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
pub fn init_edges<'db>(
    sema: &Semantics<'db, RootDatabase>,
    db: &'db RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
    inits: &mut [(model::Function, InitSource)],
    index: &HashMap<ra_ap_hir::Function, u32>,
) -> Vec<model::Call> {
    let mut agg: HashMap<(u32, u32), model::Call> = HashMap::new();

    for (node, src) in inits.iter_mut() {
        let mut sites = Vec::new();
        let mut truncated = false; // Family D — see edges()'s identical comment
        match src {
            InitSource::Const(c) => {
                let Some(csrc) = c.source(db) else { continue };
                let _ = sema.parse_or_expand(csrc.file_id);
                let Some(body) = csrc.value.body() else { continue };
                walk(sema, body.syntax(), &mut sites, 0, db, vfs, root, &mut truncated);
            }
            InitSource::Static(s) => {
                let Some(ssrc) = s.source(db) else { continue };
                let _ = sema.parse_or_expand(ssrc.file_id);
                let Some(body) = ssrc.value.body() else { continue };
                walk(sema, body.syntax(), &mut sites, 0, db, vfs, root, &mut truncated);
            }
        }
        if truncated {
            node.macro_truncated = true;
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

fn walk<'db>(
    sema: &Semantics<'db, RootDatabase>,
    node: &SyntaxNode,
    out: &mut Vec<Site>,
    depth: usize,
    db: &'db RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
    truncated: &mut bool,
) {
    if depth > MACRO_DEPTH_LIMIT {
        // Family D: this used to be silent — an expansion cut off here is
        // indistinguishable, from the caller's side, from one that simply
        // had no more calls in it. Setting `*truncated` is the entire fix:
        // it survives back up through every recursive frame that called
        // this one (see edges()/init_edges(), which record it on the
        // originating node) all the way to `Function::macro_truncated` in
        // the emitted JSON. Still a guard against pathological/adversarial
        // macro recursion (see MACRO_DEPTH_LIMIT's doc comment) — raising it
        // moves the truncation point, it does not remove the need for one.
        *truncated = true;
        return;
    }
    for n in node.descendants() {
        if let Some(mc) = ast::MacroCall::cast(n.clone()) {
            if let Some(exp) = sema.expand_macro_call(&mc) {
                walk(sema, &exp.value, out, depth + 1, db, vfs, root, truncated);
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
        // `println!("{}", w)` / `{:?}` -> `Display::fmt` / `Debug::fmt`. This
        // is NOT a syntactic method call even after macro expansion: the
        // `format_args!` builtin macro (confirmed by dumping its expanded
        // tree — see the Task B report) lowers straight to a
        // `FORMAT_ARGS_EXPR` node whose per-argument children are
        // `FormatArgsArg`, never a `CallExpr`/`MethodCallExpr` naming
        // `fmt` — the trait dispatch happens inside the format-args
        // machinery's internals, invisible to this syntax walk. No
        // `Semantics::resolve_*` exists for it at this pinned version, so
        // this resolves it the same way `resolve_bin_expr` et al. do
        // internally: from the argument's own type, not the (`{}` vs
        // `{:?}`) format spec text. Deliberately checks BOTH Display and
        // Debug impls for the argument's type regardless of which spec was
        // actually written — over-approximating is the safe direction
        // (§Non-negotiable: fails toward live), and parsing the spec to
        // pick exactly one would only ever narrow, never fix, a missed edge.
        if let Some(fargs) = ast::FormatArgsExpr::cast(n.clone()) {
            for arg in fargs.syntax().children().filter_map(ast::FormatArgsArg::cast) {
                let Some(arg_expr) = arg.expr() else { continue };
                let Some(ty) = sema.type_of_expr(&arg_expr).map(|info| info.original) else {
                    continue;
                };
                for trait_ in
                    [core_trait(db, &["fmt"], "Display"), core_trait(db, &["fmt"], "Debug")]
                        .into_iter()
                        .flatten()
                {
                    push_trait_method_edges(sema, &n, &ty, trait_, "fmt", out, db, vfs, root);
                }
            }
        }
        // `for x in it { .. }` -> `IntoIterator::into_iter(it)`, then
        // `Iterator::next(&mut <result>)` each iteration. Like format args,
        // this is HIR-level desugaring with no corresponding syntax for
        // `Semantics::resolve_*` to key off (`ForExpr` lowers straight to a
        // `match`/`loop` in `hir_def::body::lower` with no surface call
        // node), so this resolves both trait methods from the iterable's own
        // type the same way format args resolves `fmt` — by matching impls
        // to the concrete `Adt`, not by proving inference chose them. The
        // `IntoIter` associated type is looked up via
        // `normalize_trait_assoc_type` (the same API `resolve_await_to_poll`
        // above uses for `IntoFuture`'s associated type) so `Iterator::next`
        // is matched against the actual iterator type, not the iterable
        // itself — the two differ whenever `IntoIterator` isn't its own
        // `Iterator` (e.g. `Vec<T>` iterates via `std::vec::IntoIter<T>`,
        // never `Vec<T>` itself). Falls back to the iterable's own type when
        // the associated type can't be normalised (covers `impl Iterator`
        // types reached only through the core blanket `impl<I: Iterator>
        // IntoIterator for I`, where `IntoIter = Self` trivially).
        if let Some(for_expr) = ast::ForExpr::cast(n.clone()) {
            if let Some(iterable) = for_expr.iterable() {
                if let Some(src_ty) = sema.type_of_expr(&iterable).map(|info| info.original) {
                    if let Some(into_iter_trait) =
                        core_trait(db, &["iter", "traits", "collect"], "IntoIterator")
                    {
                        push_trait_method_edges(
                            sema,
                            &n,
                            &src_ty,
                            into_iter_trait,
                            "into_iter",
                            out,
                            db,
                            vfs,
                            root,
                        );
                        let into_iter_alias =
                            into_iter_trait.items(db).into_iter().find_map(|item| match item {
                                AssocItem::TypeAlias(alias)
                                    if alias.name(db).as_str() == "IntoIter" =>
                                {
                                    Some(alias)
                                }
                                _ => None,
                            });
                        let iter_ty = into_iter_alias
                            .and_then(|alias| src_ty.normalize_trait_assoc_type(db, &[], alias))
                            .unwrap_or(src_ty);
                        if let Some(iterator_trait) =
                            core_trait(db, &["iter", "traits", "iterator"], "Iterator")
                        {
                            push_trait_method_edges(
                                sema,
                                &n,
                                &iter_ty,
                                iterator_trait,
                                "next",
                                out,
                                db,
                                vfs,
                                root,
                            );
                        }
                    }
                }
            }
        }
    }
}

/// Emits a dynamic edge to `trait_`'s `method_name` for whichever impl's self
/// type matches `ty`'s own `Adt` — the same "match impls of a trait by self
/// type" shape `push_resolved`'s trait-container branch and `roots.rs`'s
/// `is_public_method` both already use, generalised for the manual
/// (non-inference-driven) trait-method lookups format args and `for`-loop
/// desugaring both need. A non-`Adt` `ty` (a generic type parameter still
/// unresolved at this call site, a primitive, or a foreign type with no
/// local impl to find anyway) yields no edge — a missed edge, not a
/// false-dead one: the safe direction.
fn push_trait_method_edges<'db>(
    sema: &Semantics<'db, RootDatabase>,
    n: &SyntaxNode,
    ty: &ra_ap_hir::Type<'db>,
    trait_: Trait,
    method_name: &str,
    out: &mut Vec<Site>,
    db: &RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
) {
    let Some(adt) = ty.as_adt() else { return };
    for imp in Impl::all_for_trait(db, trait_) {
        if imp.self_ty(db).as_adt() != Some(adt) {
            continue;
        }
        for item in imp.items(db) {
            if let AssocItem::Function(f) = item {
                if f.name(db).as_str() == method_name {
                    out.push(site(sema, n, f, true, db, vfs, root));
                }
            }
        }
    }
}

/// Locates a trait declared somewhere under `core::<path>` via a direct
/// crate-graph walk — NOT a name resolution against the workspace's own
/// `use` statements (`sema.resolve_path` et al.), which would revive exactly
/// the accidental, import-position-dependent root marking the plan's trap
/// note warns about. `testdata/desugar` deliberately has no `use` statement
/// anywhere for this reason; this lookup must not depend on one either.
fn core_trait(db: &RootDatabase, path: &[&str], name: &str) -> Option<Trait> {
    let core = Crate::all(db)
        .into_iter()
        .find(|k| matches!(k.origin(db), CrateOrigin::Lang(LangCrateOrigin::Core)))?;
    let mut module = core.root_module(db);
    for seg in path {
        module = module.children(db).find(|m| m.name(db).is_some_and(|n| n.as_str() == *seg))?;
    }
    module.scope(db, None).into_iter().find_map(|(item_name, def)| {
        if item_name.as_str() != name {
            return None;
        }
        match def {
            ScopeDef::ModuleDef(ModuleDef::Trait(t)) => Some(t),
            _ => None,
        }
    })
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
