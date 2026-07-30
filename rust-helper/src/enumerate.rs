//! Workspace-local function discovery with full node metadata.

use ra_ap_cfg::{CfgAtom, CfgExpr};
use ra_ap_hir::{
    AsAssocItem, AssocItem, AssocItemContainer, Crate, DisplayTarget, HasAttrs, HasSource,
    HasVisibility, HirDisplay, HirFileId, InFile, MacroKind, ModuleDef, Semantics, Visibility,
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
    target_dir: &AbsPath,
) -> Vec<(model::Function, ra_ap_hir::Function)> {
    let mut out = Vec::new();
    for krate in Crate::all(db) {
        if !matches!(krate.origin(db), CrateOrigin::Local { .. }) {
            continue;
        }
        for module in krate.modules(db) {
            for decl in module.declarations(db) {
                if let ModuleDef::Function(f) = decl {
                    push(db, sema, vfs, root, target_dir, f, &mut out);
                }
                if let ModuleDef::Trait(tr) = decl {
                    for item in tr.items(db) {
                        if let AssocItem::Function(f) = item {
                            push(db, sema, vfs, root, target_dir, f, &mut out);
                        }
                    }
                }
            }
            for imp in module.impl_defs(db) {
                for item in imp.items(db) {
                    if let AssocItem::Function(f) = item {
                        push(db, sema, vfs, root, target_dir, f, &mut out);
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

/// True when `file_id`'s macro kind implies the expanded content has no
/// direct user-authored counterpart in real source — the safe direction to
/// keep force-marking `generated` (see the "fail toward live" rule: over-
/// excluding a borderline case only risks *hiding* a dead row, never
/// inventing a false one). False for `file_id` that isn't a macro expansion
/// at all, and for the two kinds a `Function`'s own name/signature tokens
/// demonstrably survive macro expansion with their real span intact,
/// resolving `original_range` back to the user's own file (verified with
/// probe crates, not inferred):
///   - `MacroKind::Declarative` (`macro_rules!`, including Macros 2.0) — an
///     item-position invocation like `make_fn!(foo);` puts the real
///     identifier `foo` in the user's own call-site tokens; that identifier
///     is what becomes the generated function's name, carrying its call-site
///     span through expansion.
///   - `MacroKind::Attr` (a procedural attribute macro, e.g.
///     `#[tracing::instrument]`) decorates an already fully user-written
///     function — well-behaved attribute macros re-quote the original input
///     tokens rather than minting new ones, so the name/signature spans
///     point at the real declaration.
///
/// Every other kind stays `true`: `MacroKind::Derive`/`DeriveBuiltIn`
/// (`#[derive(Debug)]` etc.) synthesize an entire method body — including
/// its name token — with no corresponding `fn` a human ever wrote, so there
/// is no real declaration to NOT exclude. `DeclarativeBuiltIn` (built-in
/// function-like macros, chiefly `include!`) and `ProcMacro` (function-like
/// proc-macros) are left conservatively excluded too — the `target_dir`
/// anchor already independently classifies the common real-generated case
/// (`include!(concat!(env!("OUT_DIR"), ..)))`) correctly regardless, and
/// neither kind was named as an over-reach case to fix.
fn is_macro_kind_synthetic(db: &RootDatabase, file_id: HirFileId) -> bool {
    match file_id.macro_file() {
        None => false,
        Some(mc) => !matches!(mc.kind(db), MacroKind::Declarative | MacroKind::Attr),
    }
}

/// True when a `#[cfg(..)]` predicate positively, unconditionally requires
/// `test` — `#[cfg(test)]` itself, or `test` as one conjunct of an `all(..)`
/// (every conjunct must hold, so requiring `test` anywhere in the list means
/// the whole item requires it). Deliberately does NOT fire through `not(..)`
/// (`#[cfg(not(test))]` means the opposite — production-only — code, the
/// Family E concern, unrelated to this) or through a partial `any(..)`
/// (`#[cfg(any(test, feature = "x"))]` can still compile without `test`, so
/// it does not unconditionally require it). See `is_test_context` for why
/// this asymmetry (only fire on a genuinely unconditional requirement)
/// matters: a false positive here is the dangerous direction for this field.
fn cfg_requires_test(expr: &CfgExpr) -> bool {
    match expr {
        CfgExpr::Atom(CfgAtom::Flag(f)) => f.as_str() == "test",
        CfgExpr::All(exprs) => exprs.iter().any(cfg_requires_test),
        CfgExpr::Any(exprs) => !exprs.is_empty() && exprs.iter().all(cfg_requires_test),
        CfgExpr::Atom(CfgAtom::KeyValue { .. }) | CfgExpr::Not(_) | CfgExpr::Invalid => false,
    }
}

/// True when `file` (repo-relative) sits under a Cargo integration-test or
/// bench target directory — `tests/` or `benches/`, per Cargo's own target
/// discovery convention. A structural fact, not a heuristic: every file
/// Cargo discovers this way compiles ONLY into a test/bench binary, never
/// into the library or a `bin` target, regardless of any attribute on the
/// functions inside it (which is exactly why `Function::is_test` alone,
/// keyed on the `#[test]` attribute, never sees these at all — an ordinary
/// helper in `tests/foo.rs` carries no `#[test]`/`#[cfg(test)]` of its own).
/// Path-component match, not a substring match, so a module legitimately
/// named `testsuite` or a path segment like `latest/` is not caught by
/// accident.
fn in_test_or_bench_target(file: &str) -> bool {
    std::path::Path::new(file)
        .components()
        .any(|c| matches!(c.as_os_str().to_str(), Some("tests") | Some("benches")))
}

/// True when `f` is genuinely test-only — unreachable and uncallable in a
/// production (non-test) build. `Function::is_test` alone (the `#[test]`
/// attribute) covers only the harness entry point itself, not: a helper
/// inside `#[cfg(test)] mod tests { .. }` (checked here via cfg ancestry —
/// the function's own cfg, or any ancestor module's, up to the crate root),
/// nor anything in a `tests/`/`benches/` integration target (checked via
/// `in_test_or_bench_target`).
///
/// Direction: `roots::mark` treats `test:true` as NEVER a production root,
/// so a false POSITIVE here (marking real production code test-only) risks
/// hiding it from production reachability and reporting code reachable only
/// through it as false-dead — the dangerous direction for this field. A
/// false NEGATIVE reproduces defect 3 itself (a test helper laundered into a
/// production root, which then hides everything genuinely dead beneath it).
/// Both directions carry real risk, which is why every signal here is a
/// structural fact Cargo/rustc itself enforces (a directory Cargo's own
/// target discovery uses, a `#[test]`/`#[cfg(test)]` attribute actually
/// present, `cfg_requires_test`'s unconditional-only match) rather than a
/// probabilistic guess.
fn is_test_context(db: &RootDatabase, f: ra_ap_hir::Function, file: &str) -> bool {
    f.is_test(db)
        || in_test_or_bench_target(file)
        || f.attrs(db).cfgs(db).is_some_and(cfg_requires_test)
        || f.module(db)
            .path_to_root(db)
            .into_iter()
            .any(|m| m.attrs(db).cfgs(db).is_some_and(cfg_requires_test))
}

/// A function's `symbol`, qualified enough that `(pkg, symbol)` is unique —
/// bare `f.name(db)` is not: `testdata/libonly` has both a trait
/// declaration's own method (`trait ReTr { fn re_m(&self); }`) and that
/// trait's impl method (`impl ReTr for ReS { fn re_m(&self) {} }`) in the
/// same module, both named `re_m`, giving two distinct nodes the identical
/// `symbol="re_m", pkg="libonly::reexported_trait"` pair — Architext's node
/// id is `slug(pkg + "-" + symbol)` with positional `-2`/`-3` collision
/// suffixes, so an unrelated edit elsewhere in the file can silently renumber
/// which node owns which id. Mirrors Go's `T.Method` receiver-qualification,
/// generalised to Rust's three assoc-item shapes:
///   - a free function or an assoc item with no container (never happens,
///     just the fallback): bare name, unchanged — Go's bare function names
///     have the same shape and the same uniqueness argument.
///   - a trait DECLARATION's own method: `"{Trait}::{name}"` — there is no
///     self type to qualify with (`fn re_m` inside `trait ReTr { .. }` names
///     no `Self`), so the trait itself is the only available qualifier, and
///     it is exactly what's needed to distinguish this from the impl below.
///   - an INHERENT impl method: `"{SelfType}::{name}"` — direct analogue of
///     Go's `T.Method`.
///   - a TRAIT impl method: `"<{SelfType} as {Trait}>::{name}"` — Rust's own
///     fully-qualified syntax, deliberately more than just `{SelfType}::
///     {name}` because one self type can implement multiple traits sharing a
///     method name (`Display::fmt` and `Debug::fmt` on the same struct are
///     both `fmt`); the self-type-only form would silently reintroduce the
///     exact collision this fix exists to close.
fn qualified_symbol(db: &RootDatabase, f: ra_ap_hir::Function, display_target: DisplayTarget) -> String {
    let name = f.name(db).as_str().to_owned();
    let Some(assoc) = f.as_assoc_item(db) else { return name };
    match assoc.container(db) {
        AssocItemContainer::Trait(tr) => format!("{}::{name}", tr.name(db).as_str()),
        AssocItemContainer::Impl(imp) => {
            let self_ty = imp.self_ty(db).display(db, display_target).to_string();
            match imp.trait_(db) {
                Some(tr) => format!("<{self_ty} as {}>::{name}", tr.name(db).as_str()),
                None => format!("{self_ty}::{name}"),
            }
        }
    }
}

fn push(
    db: &RootDatabase,
    sema: &Semantics<'_, RootDatabase>,
    vfs: &Vfs,
    root: &AbsPath,
    target_dir: &AbsPath,
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
    // Defect 3 fix: `f.is_test(db)` alone only sees the `#[test]` attribute —
    // see `is_test_context`'s doc comment for what that misses and why.
    // Computed before `file` is moved into the struct literal below.
    let test = is_test_context(db, f, &file);

    let id = out.len() as u32;
    out.push((
        model::Function {
            id,
            // Defect 5 fix: `f.name(db)` alone is not unique within (pkg,
            // symbol) — see `qualified_symbol`'s doc comment.
            symbol: qualified_symbol(db, f, display_target),
            pkg: module_path(db, f),
            file,
            line: line + 1, // line_index is 0-based; magma reports 1-based
            column: column + 1, // same: line_index col is 0-based, rustc's is 1-based
            kind: if f.self_param(db).is_some() { "method" } else { "func" }.to_owned(),
            exported: f.visibility(db) == Visibility::Public,
            test,
            root: false,   // Task 5
            bench: f.is_bench(db),
            // H1 fix: anchored to the workspace's OWN target directory
            // (resolved via `cargo metadata`, see main.rs::discover_target_dir),
            // not an unanchored substring match on the absolute path. The prior
            // `p.contains("/target/") || p.contains("/build/")` matched an
            // ordinary user module named `build`, or any checkout path merely
            // containing the segment `/build/` (e.g. a fixture copied under
            // `/tmp/build/proj`) — silently reclassifying every function in the
            // crate as generated and, downstream, excluding all of them from
            // the oracle comparison (see oracle-diff.sh's H1 refusal for what
            // catches the case this heuristic itself can't).
            //
            // Defect 4 fix: `is_macro_origin` alone used to force `generated:
            // true` for EVERY macro-expanded item, which over-reaches — a
            // `macro_rules!` invocation in the user's own hand-written source
            // (e.g. `make_fn!(foo);`) and a proc-macro attribute like
            // `#[tracing::instrument]` decorating a fully user-written
            // function both resolve, via `original_range` above, back to the
            // user's own real source file, never under `target_dir`. Forcing
            // them generated regardless hid genuinely dead, hand-written code
            // from every dead-code view. `is_macro_kind_synthetic` narrows
            // this to macro kinds whose expanded content has no real,
            // user-authored counterpart to point at (a derive-synthesized
            // method body, for instance) — see its doc comment for exactly
            // which kinds are excluded and why each one is safe to exclude.
            generated: is_macro_kind_synthetic(db, src.file_id)
                || vfs
                    .file_path(file_id)
                    .as_path()
                    .map(|p| p.starts_with(target_dir))
                    .unwrap_or(false),
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
