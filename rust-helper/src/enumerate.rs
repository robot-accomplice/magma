//! Workspace-local function discovery with full node metadata.

use ra_ap_hir::{AssocItem, Crate, HasSource, HasVisibility, ModuleDef, Visibility};
use ra_ap_ide_db::base_db::CrateOrigin;
use ra_ap_ide_db::RootDatabase;
use ra_ap_paths::AbsPath;
use ra_ap_syntax::ast::HasName;
use ra_ap_syntax::AstNode;
use ra_ap_vfs::Vfs;

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

    let file = match vfs.file_path(file_id).as_path() {
        Some(abs) => match abs.strip_prefix(root) {
            Some(rel) => rel.as_str().to_owned(),
            None => abs.to_string(),
        },
        None => vfs.file_path(file_id).to_string(),
    };

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
            generated: false, // Task 7
        },
        f,
    ));
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
