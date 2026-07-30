//! Workspace-local function discovery with full node metadata.

use ra_ap_hir::{
    AsAssocItem, AssocItem, AssocItemContainer, Crate, DisplayTarget, HasAttrs, HasSource,
    HasVisibility, HirDisplay, InFile, ModuleDef, Semantics, Visibility,
};
use ra_ap_ide_db::base_db::CrateOrigin;
use ra_ap_ide_db::line_index::{TextSize, WideEncoding};
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
    sema: &Semantics<'_, RootDatabase>,
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
                    push(db, sema, vfs, root, f, &mut out);
                }
                if let ModuleDef::Trait(tr) = decl {
                    for item in tr.items(db) {
                        if let AssocItem::Function(f) = item {
                            push(db, sema, vfs, root, f, &mut out);
                        }
                    }
                }
            }
            for imp in module.impl_defs(db) {
                for item in imp.items(db) {
                    if let AssocItem::Function(f) = item {
                        push(db, sema, vfs, root, f, &mut out);
                    }
                }
            }
        }
    }
    out
}

/// Resolves a UTF-8 byte `offset` in `file_id` to a (0-based line, 0-based
/// *character* column) pair, matching rustc's own column convention rather
/// than ra_ap's. `LineIndex::line_col` returns a UTF-8 *byte* column
/// (`line-index`'s own doc comment: "Zero-based UTF-8 offset"), while
/// rustc's diagnostic columns count Unicode scalar values (characters) — the
/// two agree on ASCII lines but diverge on any line carrying multi-byte
/// UTF-8 text, which broke the oracle harness's (file, line, column) key on
/// such lines (Task 17). `to_wide(WideEncoding::Utf32, ..)` performs exactly
/// this byte-to-character conversion. `Utf32`, not `Utf16` (which is what
/// LSP wide-column callers elsewhere in ra_ap want), is required: rustc
/// counts characters, and UTF-16 would undercount by one per surrogate pair
/// for anything outside the BMP (e.g. emoji) — `WideEncoding::measure`
/// confirms `Utf32 => text.chars().count()`, `Utf16 =>
/// text.encode_utf16().count()`.
fn char_line_col(db: &RootDatabase, file_id: FileId, offset: TextSize) -> (u32, u32) {
    let index = ra_ap_ide_db::line_index(db, file_id);
    let line_col = index.line_col(offset);
    let wide = index
        .to_wide(WideEncoding::Utf32, line_col)
        .expect("a LineCol produced by the same LineIndex always converts");
    (line_col.line, wide.col)
}

fn push(
    db: &RootDatabase,
    sema: &Semantics<'_, RootDatabase>,
    vfs: &Vfs,
    root: &AbsPath,
    f: ra_ap_hir::Function,
    out: &mut Vec<(model::Function, ra_ap_hir::Function)>,
) {
    let Some(src) = f.source(db) else { return };
    let Some(name_node) = src.value.name() else { return };

    // `src.file_id.file_id()` is None when the definition itself comes from
    // macro expansion — notably `include!(concat!(env!("OUT_DIR"), ...))`,
    // the standard build-script codegen pattern. Previously this bailed out
    // entirely, silently dropping the node (and, since walk::edges only
    // walks enumerated functions, every edge reachable through it) — magma
    // then reported live generated code as dead. Instead, cache this
    // function's macro-expansion tree into `sema` (parse_or_expand handles
    // both a real file and a macro file uniformly) and ask Semantics to map
    // the name node's position back out of the expansion. For `include!`
    // this resolves via real span maps to the actual generated file and
    // position; for other macro-item shapes it falls back to the macro call
    // site — either way a human-navigable location, never a synthetic one.
    let is_macro_origin = src.file_id.is_macro();
    let (file_id, line, column) = match src.file_id.file_id() {
        Some(efid) => {
            let file_id = efid.file_id(db);
            let (line, column) =
                char_line_col(db, file_id, name_node.syntax().text_range().start());
            (file_id, line, column)
        }
        None => {
            sema.parse_or_expand(src.file_id);
            let range = sema.original_range(name_node.syntax());
            let file_id = range.file_id.file_id(db);
            let (line, column) = char_line_col(db, file_id, range.range.start());
            (file_id, line, column)
        }
    };

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
            column: column + 1, // same: line_index col is 0-based, rustc's is 1-based
            kind: if f.self_param(db).is_some() { "method" } else { "func" }.to_owned(),
            exported: f.visibility(db) == Visibility::Public,
            test: f.is_test(db),
            root: false,   // Task 5
            bench: f.is_bench(db),
            generated: {
                let p = vfs.file_path(file_id).to_string();
                is_macro_origin || p.contains("/target/") || p.contains("/build/")
            },
            signature: model::Signature { params, results },
            doc,
            trait_impl: trait_impl_loc(db, sema, vfs, root, f),
        },
        f,
    ));
}

/// (file, line, column) locations the oracle-diff harness needs to key its
/// trait-impl cascade-suppression check collision-proof (Task 13). `None`
/// unless `f` is an assoc item of a *trait* impl (`impl Trait for Type { .. }`) — an
/// inherent-impl method, a free function, or a trait declaration's own
/// method never participates in rustc's cascade-suppression behaviour this
/// exists to key off of (see the module comment in `roots.rs` for the same
/// trait-impl/inherent-impl/trait-decl split, and `scripts/oracle-diff.sh`
/// for how the oracle harness consumes this).
fn trait_impl_loc(
    db: &RootDatabase,
    sema: &Semantics<'_, RootDatabase>,
    vfs: &Vfs,
    root: &AbsPath,
    f: ra_ap_hir::Function,
) -> Option<model::TraitImpl> {
    let AssocItemContainer::Impl(imp) = f.as_assoc_item(db)?.container(db) else { return None };
    let tr = imp.trait_(db)?; // None => inherent impl, not a trait impl.

    let trait_src = tr.source(db)?;
    let trait_decl = decl_loc(db, sema, vfs, root, trait_src)?;

    // The self type only gates the cascade when it can independently receive
    // its own dead_code diagnostic: a workspace-local struct/enum/union.
    // `as_adt()` returns `None` outright for a reference/tuple/builtin shape
    // (verified: `impl Tr for &S` gives `as_adt() == None` even though `S`
    // itself is workspace-local — there is no "reference" ADT to find). For a
    // generic instantiation of an external type it resolves to that type's
    // OWN ADT rather than the argument's — verified: `impl Tr for Vec<S>`
    // gives `as_adt() == Some(Vec)`, not `S` — which then correctly fails the
    // `CrateOrigin::Local` check below since `Vec` is std's, not this
    // workspace's. Either path lands on `NotEligible`, just via different
    // reasoning; a type from another (already-local) crate hits the same
    // `Local` check directly. None of these can receive a dead_code
    // diagnostic from this workspace's own `cargo check`, however dead the
    // impl is, so none of them may be required to independently show "dead"
    // for the cascade to apply — that is the Task 13 scoping-correction
    // criterion. `Unresolved` (as opposed to `NotEligible`) is reserved for a
    // *found* local `Adt` whose own declaration location lookup itself
    // failed — it must never license the cascade the way genuine
    // ineligibility does (see `model::SelfType`).
    let self_type = match imp.self_ty(db).as_adt() {
        None => model::SelfType::NotEligible,
        Some(adt) => {
            if !matches!(adt.module(db).krate(db).origin(db), CrateOrigin::Local { .. }) {
                model::SelfType::NotEligible
            } else {
                match adt.source(db).and_then(|src| decl_loc(db, sema, vfs, root, src)) {
                    Some(loc) => {
                        model::SelfType::Local { file: loc.file, line: loc.line, column: loc.column }
                    }
                    None => model::SelfType::Unresolved,
                }
            }
        }
    };

    Some(model::TraitImpl { trait_decl, self_type })
}

/// Resolves an item's own declaration to a repo-relative (file, 1-based
/// line, 1-based column), using its `name` node's position — same real-file/
/// macro-expansion handling `push` uses for a function's own declaration,
/// generalised over any named HIR item (`ra_ap_syntax::ast::HasName`). The
/// column (not just the line) matters: two distinct declarations can share
/// one source line (see `model::Function::column`), and rustc's own
/// diagnostic column always points at the exact name token, never just the
/// line — so a caller matching on (file, line, column) against rustc's own
/// `column_start` gets an exact-token match, not merely a same-line one.
fn decl_loc<N: AstNode + HasName>(
    db: &RootDatabase,
    sema: &Semantics<'_, RootDatabase>,
    vfs: &Vfs,
    root: &AbsPath,
    src: InFile<N>,
) -> Option<model::Loc> {
    let name_node = src.value.name()?;
    let (file_id, line, column) = match src.file_id.file_id() {
        Some(efid) => {
            let file_id = efid.file_id(db);
            let (line, column) =
                char_line_col(db, file_id, name_node.syntax().text_range().start());
            (file_id, line, column)
        }
        None => {
            sema.parse_or_expand(src.file_id);
            let range = sema.original_range(name_node.syntax());
            let file_id = range.file_id.file_id(db);
            let (line, column) = char_line_col(db, file_id, range.range.start());
            (file_id, line, column)
        }
    };
    Some(model::Loc {
        file: repo_relative_path(vfs, root, file_id),
        line: line + 1,
        column: column + 1,
    })
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
