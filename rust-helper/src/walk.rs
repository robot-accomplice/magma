//! Body walking with macro descent. Never use Analysis::outgoing_calls: it
//! silently omits macro-generated edges, which produces false dead code.

use std::collections::HashMap;

use ra_ap_hir::{
    AsAssocItem, AssocItem, AssocItemContainer, HasSource, Impl, PathResolution, Semantics,
};
use ra_ap_ide_db::RootDatabase;
use ra_ap_paths::AbsPath;
use ra_ap_syntax::{ast, AstNode, SyntaxNode};
use ra_ap_vfs::Vfs;

use crate::enumerate;
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
        if let Some(efid) = src.file_id.file_id() {
            let _ = sema.parse(efid);
        }
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
        if let Some(mcall) = ast::MethodCallExpr::cast(n.clone()) {
            if let Some(f) = sema.resolve_method_call(&mcall) {
                match f.as_assoc_item(db).map(|assoc| assoc.container(db)) {
                    Some(AssocItemContainer::Trait(t)) => {
                        // `dyn Trait` receiver: resolve_method_call gives back the
                        // trait's own declared function, which is never enumerated
                        // (no node in `index`), so a direct edge would silently
                        // vanish and invent dead code for every impl. Over-approximate
                        // instead, mirroring Go's RTA: emit one dynamic edge to every
                        // impl of this trait method in the workspace. Extra edges
                        // under-report dead code (safe); missing edges invent it.
                        let name = f.name(db);
                        for imp in Impl::all_for_trait(db, t) {
                            for item in imp.items(db) {
                                if let AssocItem::Function(impl_fn) = item {
                                    if impl_fn.name(db) == name {
                                        out.push(site(sema, &n, impl_fn, true, db, vfs, root));
                                    }
                                }
                            }
                        }
                    }
                    _ => out.push(site(sema, &n, f, false, db, vfs, root)),
                }
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
    vfs: &Vfs,
    root: &AbsPath,
) -> Site {
    let range = sema.original_range(n);
    let file_id = range.file_id.file_id(db);
    // line_index is 0-based; magma reports 1-based (matches enumerate.rs).
    let line = ra_ap_ide_db::line_index(db, file_id).line_col(range.range.start()).line + 1;
    Site { to, file: enumerate::repo_relative_path(vfs, root, file_id), line, dynamic }
}
