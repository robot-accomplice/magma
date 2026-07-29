//! Workspace-local function discovery with full node metadata.

use ra_ap_hir::{
    AssocItem, Crate, DisplayTarget, HasAttrs, HasSource, HasVisibility, HirDisplay, ModuleDef,
    Visibility,
};
use ra_ap_ide_db::base_db::CrateOrigin;
use ra_ap_ide_db::RootDatabase;
use ra_ap_paths::AbsPath;
use ra_ap_syntax::ast::HasName;
use ra_ap_syntax::AstNode;
use ra_ap_vfs::{FileId, Vfs};

use crate::model;

/// Every function in workspace-member crates, paired with its hir handle.
///
/// The CrateOrigin::Local filter is not optional: without it this returns the
/// entire dependency closure plus std (118,081 vs 8,437 on roboticus-rust),
/// which floods the graph and produces false dead code.
pub fn collect(
    db: &RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
) -> Vec<(model::Function, ra_ap_hir::Function)> {
    let mut out = Vec::new();
    for krate in Crate::all(db) {
        if !matches!(krate.origin(db), CrateOrigin::Local { .. }) {
            continue;
        }
        for module in krate.modules(db) {
            for decl in module.declarations(db) {
                if let ModuleDef::Function(f) = decl {
                    push(db, vfs, root, f, &mut out);
                }
            }
            for imp in module.impl_defs(db) {
                for item in imp.items(db) {
                    if let AssocItem::Function(f) = item {
                        push(db, vfs, root, f, &mut out);
                    }
                }
            }
        }
    }
    out
}

fn push(
    db: &RootDatabase,
    vfs: &Vfs,
    root: &AbsPath,
    f: ra_ap_hir::Function,
    out: &mut Vec<(model::Function, ra_ap_hir::Function)>,
) {
    let Some(src) = f.source(db) else { return };
    let Some(name_node) = src.value.name() else { return };
    let Some(efid) = src.file_id.file_id() else { return };
    let file_id = efid.file_id(db);

    let line = ra_ap_ide_db::line_index(db, file_id)
        .line_col(name_node.syntax().text_range().start())
        .line;

    let file = repo_relative_path(vfs, root, file_id);

    let display_target = DisplayTarget::from_crate(db, f.module(db).krate(db).into());
    let params: Vec<model::Param> = f
        .params_without_self(db)
        .into_iter()
        .map(|p| model::Param {
            name: p.name(db).map(|n| n.as_str().to_owned()),
            ty: p.ty().display(db, display_target).to_string(),
        })
        .collect();
    let ret_str = f.ret_type(db).display(db, display_target).to_string();
    // Rust always has exactly one return type; "()" means no meaningful result,
    // which magma represents as an empty results list (matching Go's no-return).
    let results =
        if ret_str == "()" { Vec::new() } else { vec![model::Result_ { ty: ret_str }] };
    let doc = f.hir_docs(db).map(|d| first_sentence(d.docs()));

    let id = out.len() as u32;
    out.push((
        model::Function {
            id,
            symbol: f.name(db).as_str().to_owned(),
            pkg: module_path(db, f),
            file,
            line: line + 1, // line_index is 0-based; magma reports 1-based
            kind: if f.self_param(db).is_some() { "method" } else { "func" }.to_owned(),
            exported: f.visibility(db) == Visibility::Public,
            test: f.is_test(db),
            root: false,   // Task 5
            bench: f.is_bench(db),
            generated: {
                let p = vfs.file_path(file_id).to_string();
                p.contains("/target/") || p.contains("/build/")
            },
            signature: model::Signature { params, results },
            doc,
        },
        f,
    ));
}

/// Repo-relative path for `file_id`, computed the same way for node metadata
/// (here) and edge call-site metadata (walk.rs) — the two must agree.
pub(crate) fn repo_relative_path(vfs: &Vfs, root: &AbsPath, file_id: FileId) -> String {
    match vfs.file_path(file_id).as_path() {
        Some(abs) => match abs.strip_prefix(root) {
            Some(rel) => rel.as_str().to_owned(),
            None => abs.to_string(),
        },
        None => vfs.file_path(file_id).to_string(),
    }
}

/// First sentence of a doc comment — a pointer, not a payload. Mirrors Go's
/// use of doc.Synopsis.
fn first_sentence(text: &str) -> String {
    let trimmed = text.trim();
    match trimmed.find(". ") {
        Some(i) => trimmed[..=i].to_owned(),
        None => trimmed.lines().next().unwrap_or("").trim_end_matches('.').to_owned() + ".",
    }
}

/// "crate::module::path" for the function's containing module.
fn module_path(db: &RootDatabase, f: ra_ap_hir::Function) -> String {
    let module = f.module(db);
    let krate = module.krate(db).display_name(db).map(|d| d.to_string()).unwrap_or_default();
    let mut parts: Vec<String> = module
        .path_to_root(db)
        .into_iter()
        .rev()
        .filter_map(|m| m.name(db).map(|n| n.as_str().to_owned()))
        .collect();
    parts.insert(0, krate);
    parts.join("::")
}
