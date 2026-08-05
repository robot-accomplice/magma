//! Workspace-local function discovery with full node metadata.

use ra_ap_cfg::{CfgAtom, CfgExpr};
use ra_ap_hir::{
    AsAssocItem, AssocItem, AssocItemContainer, Const, Crate, DisplayTarget, HasAttrs, HasSource,
    HasVisibility, HirDisplay, HirFileId, InFile, MacroKind, Module, ModuleDef, Semantics, Static,
    Visibility,
};
use ra_ap_ide_db::base_db::CrateOrigin;
use ra_ap_ide_db::line_index::{TextSize, WideEncoding};
use ra_ap_ide_db::RootDatabase;
use ra_ap_paths::AbsPath;
use ra_ap_syntax::ast::HasName;
use ra_ap_syntax::AstNode;
use ra_ap_vfs::{FileId, Vfs};

use crate::ctx::Ctx;
use crate::model;

/// Every function in workspace-member crates, paired with its hir handle.
///
/// The CrateOrigin::Local filter is not optional: without it this returns the
/// entire dependency closure plus std (118,081 vs 8,437 on roboticus-rust),
/// which floods the graph and produces false dead code.
pub fn collect(cx: Ctx<'_, '_>) -> Vec<(model::Function, ra_ap_hir::Function)> {
    let Ctx { db, .. } = cx;
    let mut out = Vec::new();
    for krate in Crate::all(db) {
        if !matches!(krate.origin(db), CrateOrigin::Local { .. }) {
            continue;
        }
        for module in krate.modules(db) {
            for decl in module.declarations(db) {
                if let ModuleDef::Function(f) = decl {
                    push(cx, f, &mut out);
                }
                if let ModuleDef::Trait(tr) = decl {
                    for item in tr.items(db) {
                        if let AssocItem::Function(f) = item {
                            push(cx, f, &mut out);
                        }
                    }
                }
            }
            for imp in module.impl_defs(db) {
                for item in imp.items(db) {
                    if let AssocItem::Function(f) = item {
                        push(cx, f, &mut out);
                    }
                }
            }
        }
    }
    out
}

/// A const or static item with an initializer expression — the two shapes
/// Family C's synthesized nodes wrap (see `collect_inits`). `walk::init_edges`
/// re-derives the initializer's syntax tree from this handle inside its own
/// `Semantics`/`attach_db` context, exactly like `walk::edges` re-derives a
/// function's body from the `ra_ap_hir::Function` paired alongside each
/// `model::Function` in `funcs`. `Const` covers both a top-level const and
/// an associated const (inherent impl, trait impl, or a trait's own
/// default-valued declaration) — Rust admits associated consts but never
/// associated statics, so `Static` is only ever a top-level module item.
pub enum InitSource {
    Const(Const),
    Static(Static),
}

/// Every const/static/associated-const item WITH an initializer expression
/// in workspace-member crates (Family C: `walk::edges` only ever visits
/// `f.source(db).value.body()` for an enumerated function, so a call written
/// in a const/static/associated-const initializer — never itself a function
/// body, nor contained in one — was structurally invisible; see
/// `push_const`/`push_static` for why a declaration-only item, e.g. a
/// trait's own const with no default or an `extern` static, is skipped
/// rather than emitted as a node with no possible outgoing edges). Each
/// synthesized node gets its own id continuing the id space `next_id` starts
/// at — the caller passes `funcs.len()`, the real-function count, so ids
/// stay globally unique across the whole `functions` array `main.rs` emits;
/// real functions never renumber, so this is purely additive to the id
/// space, not a reassignment.
///
/// Deliberately a second, independent crate/module/impl traversal rather
/// than folding into `collect()`'s existing loop: `collect()` is shared with
/// a concurrently active task's `walk.rs` desugaring work, and keeping this
/// function untouched avoids any merge risk. The small duplication of the
/// traversal shape (crate -> module -> declarations/impl_defs) is the
/// accepted cost.
///
/// Anonymous `const _: T = expr;` items (no name to qualify a symbol from)
/// are skipped in `push_const` — a distinct, rarer pattern absent from every
/// fixture this task touches; naming them would need positional numbering,
/// reintroducing exactly the collision risk `qualified_init_symbol` exists
/// to avoid. Not a NEW soundness gap: a call inside such a const's
/// initializer simply stays unwalked, the same (pre-existing, safe-direction
/// under-report, never a false-dead invention) gap every other
/// un-enumerated item already has.
pub fn collect_inits(cx: Ctx<'_, '_>, next_id: u32) -> Vec<(model::Function, InitSource)> {
    let Ctx { db, .. } = cx;
    let mut out = Vec::new();
    let mut next_id = next_id;
    for krate in Crate::all(db) {
        if !matches!(krate.origin(db), CrateOrigin::Local { .. }) {
            continue;
        }
        for module in krate.modules(db) {
            for decl in module.declarations(db) {
                match decl {
                    ModuleDef::Const(c) => push_const(cx, c, &mut next_id, &mut out),
                    ModuleDef::Static(s) => push_static(cx, s, &mut next_id, &mut out),
                    ModuleDef::Trait(tr) => {
                        for item in tr.items(db) {
                            if let AssocItem::Const(c) = item {
                                push_const(cx, c, &mut next_id, &mut out);
                            }
                        }
                    }
                    _ => {}
                }
            }
            for imp in module.impl_defs(db) {
                for item in imp.items(db) {
                    if let AssocItem::Const(c) = item {
                        push_const(cx, c, &mut next_id, &mut out);
                    }
                }
            }
        }
    }
    out
}

/// Same test-context detection as `is_test_context`, generalised to a
/// const/static item's own cfg + ancestry. There is no `#[test]`-attribute
/// analogue for a const/static, so unlike `is_test_context` this has no
/// `f.is_test(db)` term; the other two signals — `tests`/`benches`
/// directory membership, and an unconditional `#[cfg(test)]` requirement
/// anywhere from the item itself up to the crate root — carry over
/// unchanged, for the same fail-safe-toward-live reasoning documented on
/// `is_test_context`.
fn is_init_cfg_test(
    db: &RootDatabase,
    item_cfg_requires_test: bool,
    module: Module,
    file: &str,
) -> bool {
    in_test_or_bench_target(file)
        || item_cfg_requires_test
        || module
            .path_to_root(db)
            .into_iter()
            .any(|m| m.attrs(db).cfgs(db).is_some_and(cfg_requires_test))
}

/// Everything the shared builder below needs that a `const` and a `static`
/// supply DIFFERENTLY. The two were near-identical 100-line copies —
/// `push_const` and `push_static` differed in seven mechanical places and
/// nothing else — with a doc comment justifying the duplication as avoiding
/// "merge risk" against a concurrently active task. That task ended; the
/// duplication outlived its reason and every node-metadata change since has
/// had to be made twice and kept consistent by hand (`test_entry` was).
struct InitFacts {
    name: String,
    /// The declaration's own `HirFileId`, kept because the macro-kind check
    /// needs it — the resolved real-file `FileId` cannot answer whether the
    /// item came from an expansion.
    hir_file: HirFileId,
    /// `ast::Name` is the SAME type for both `ast::Const` and `ast::Static`,
    /// which is what lets the location resolution live in the builder instead
    /// of being copied into each caller.
    name_node: ra_ap_syntax::ast::Name,
    module: Module,
    /// Rendered here rather than in the builder because `Const::ty` and
    /// `Static::ty` are distinct methods.
    ty: String,
    exported: bool,
    /// `Some` only for an associated const; Rust has no associated statics.
    assoc: Option<AssocItem>,
    doc: Option<String>,
    /// The item's own `#[cfg(..)]` requiring `test`. Precomputed because it is
    /// the one term of `is_init_cfg_test` that needs the ITEM, while the
    /// others need the file path the builder computes.
    item_cfg_requires_test: bool,
    source: InitSource,
}

/// Builds a synthesized `model::Function` node for `c`'s initializer, if it
/// has one, and pushes it (paired with `InitSource::Const(c)`) onto `out`.
/// Mirrors `push`'s real-file/macro-expansion location handling exactly
/// (duplicated rather than shared, same precedent as `decl_loc`'s own doc
/// comment: needing the raw `FileId` back for the `generated` check, not
/// just a repo-relative string, is what stops this from calling `decl_loc`
/// directly).
fn push_const(
    cx: Ctx<'_, '_>,
    c: Const,
    next_id: &mut u32,
    out: &mut Vec<(model::Function, InitSource)>,
) {
    let Ctx { db, .. } = cx;
    // No initializer -> nothing for walk.rs to walk and no `from` endpoint
    // is ever needed (a trait's own const DECLARATION with no default,
    // `const N: i32;`, has no body).
    if c.value(db).is_none() {
        return;
    }
    // Anonymous `const _: T = expr;` -- see collect_inits' doc comment.
    let Some(name) = c.name(db) else { return };
    let Some(src) = c.source(db) else { return };
    let Some(name_node) = src.value.name() else {
        return;
    };
    let module = c.module(db);
    let display_target = DisplayTarget::from_crate(db, module.krate(db).into());
    push_init(
        cx,
        InitFacts {
            name: name.as_str().to_owned(),
            hir_file: src.file_id,
            name_node,
            module,
            ty: c.ty(db).display(db, display_target).to_string(),
            exported: c.visibility(db) == Visibility::Public,
            assoc: c.as_assoc_item(db),
            doc: c.hir_docs(db).map(|d| first_sentence(d.docs())),
            item_cfg_requires_test: c.attrs(db).cfgs(db).is_some_and(cfg_requires_test),
            source: InitSource::Const(c),
        },
        next_id,
        out,
    );
}

/// `Static` counterpart. Only two things differ from `push_const` and both are
/// properties of the language: `Static::name` returns a bare `Name` rather than
/// `Option<Name>` (there is no anonymous static), and a static is never an
/// associated item (Rust has no associated statics), so `assoc` is always
/// `None` and its symbol takes `qualify_stem`'s unqualified fallback.
fn push_static(
    cx: Ctx<'_, '_>,
    s: Static,
    next_id: &mut u32,
    out: &mut Vec<(model::Function, InitSource)>,
) {
    let Ctx { db, .. } = cx;
    // `extern "C" { static FOO: i32; }` has no initializer -- skip, mirrors
    // push_const's trait-const-declaration-with-no-default case.
    if s.value(db).is_none() {
        return;
    }
    let Some(src) = s.source(db) else { return };
    let Some(name_node) = src.value.name() else {
        return;
    };
    let module = s.module(db);
    let display_target = DisplayTarget::from_crate(db, module.krate(db).into());
    push_init(
        cx,
        InitFacts {
            name: s.name(db).as_str().to_owned(),
            hir_file: src.file_id,
            name_node,
            module,
            ty: s.ty(db).display(db, display_target).to_string(),
            exported: s.visibility(db) == Visibility::Public,
            assoc: None,
            doc: s.hir_docs(db).map(|d| first_sentence(d.docs())),
            item_cfg_requires_test: s.attrs(db).cfgs(db).is_some_and(cfg_requires_test),
            source: InitSource::Static(s),
        },
        next_id,
        out,
    );
}

/// The node construction both initializer kinds share. Mirrors `push`'s
/// real-file/macro-expansion location handling exactly (still duplicated
/// against `push` itself, for the reason `decl_loc` records: needing the raw
/// `FileId` back for the `generated` check, not just a repo-relative string).
fn push_init(
    cx: Ctx<'_, '_>,
    facts: InitFacts,
    next_id: &mut u32,
    out: &mut Vec<(model::Function, InitSource)>,
) {
    let Ctx {
        db,
        sema,
        vfs,
        root,
        target_dir,
    } = cx;
    let (file_id, line, column) = match facts.hir_file.file_id() {
        Some(efid) => {
            let file_id = efid.file_id(db);
            let (line, column) =
                char_line_col(db, file_id, facts.name_node.syntax().text_range().start());
            (file_id, line, column)
        }
        None => {
            sema.parse_or_expand(facts.hir_file);
            let range = sema.original_range(facts.name_node.syntax());
            let file_id = range.file_id.file_id(db);
            let (line, column) = char_line_col(db, file_id, range.range.start());
            (file_id, line, column)
        }
    };
    let file = repo_relative_path(vfs, root, file_id);

    let display_target = DisplayTarget::from_crate(db, facts.module.krate(db).into());
    let test = is_init_cfg_test(db, facts.item_cfg_requires_test, facts.module, &file);
    let results = if facts.ty == "()" {
        Vec::new()
    } else {
        vec![model::Result_ { ty: facts.ty }]
    };

    let id = *next_id;
    *next_id += 1;
    out.push((
        model::Function {
            id,
            symbol: qualified_init_symbol(db, &facts.name, facts.assoc, display_target),
            pkg: module_path_of(db, facts.module),
            file,
            line: line + 1,
            column: column + 1,
            kind: "init".to_owned(),
            exported: facts.exported,
            test,
            // A const/static item cannot carry #[test]; see model::Function::test_entry.
            test_entry: false,
            // Runs whenever the enclosing (non-test) binary starts, so
            // whatever it calls is genuinely reachable -- same reasoning as
            // Go's synthesized `init#N` nodes, always roots. `!test`, not
            // unconditional `true`: test-only code follows the same
            // never-a-production-root rule `roots::mark` already applies to
            // every other test item (see its doc comment) -- the all-roots
            // view is where a #[cfg(test)] item's own root status belongs.
            root: !test,
            // No #[bench] analogue exists for a const/static item.
            bench: false,
            generated: is_macro_kind_synthetic(db, facts.hir_file)
                || vfs
                    .file_path(file_id)
                    .as_path()
                    .map(|p| p.starts_with(target_dir))
                    .unwrap_or(false),
            // Family D: never truncated at construction time -- walk.rs sets
            // this true later, in place, only if its macro-depth guard fires
            // while walking THIS node's own initializer expression.
            macro_truncated: false,
            signature: model::Signature {
                params: Vec::new(),
                results,
            },
            doc: facts.doc,
            // Never an assoc item of a *trait impl* method -- `trait_impl`
            // exists only to key the oracle harness's method-cascade check.
            trait_impl: None,
        },
        facts.source,
    ));
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
fn qualified_symbol(
    db: &RootDatabase,
    f: ra_ap_hir::Function,
    display_target: DisplayTarget,
) -> String {
    let name = f.name(db).as_str().to_owned();
    qualify_stem(db, &name, f.as_assoc_item(db), display_target)
}

/// The container-qualification logic shared by `qualified_symbol` (a
/// function's own symbol) and `qualified_init_symbol` (a synthesized
/// const/static initializer node's symbol, Family C) — see
/// `qualified_symbol`'s doc comment above for the full collision rationale
/// each of the three branches (bare / trait-declaration / impl) addresses;
/// that reasoning is identical for a const/static/associated-const's own
/// name, since Rust's three assoc-item shapes (free item, inherent-impl
/// item, trait-impl item) apply to `const` exactly as they do to `fn`.
fn qualify_stem(
    db: &RootDatabase,
    name: &str,
    assoc: Option<AssocItem>,
    display_target: DisplayTarget,
) -> String {
    let Some(assoc) = assoc else {
        return name.to_owned();
    };
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

/// Same collision-avoidance as `qualified_symbol`, for a synthesized
/// const/static/associated-const initializer node (Family C — see
/// `collect_inits`). Suffixed `#init`: `#` cannot appear in a Rust
/// identifier or in anything `qualify_stem` produces, so an initializer
/// node's symbol can never collide with a real function's — e.g. a
/// top-level `const VALUE: i32 = ..;` becomes `"VALUE#init"`, an inherent
/// associated const `impl W { const K: i32 = ..; }` becomes `"W::K#init"`.
/// Deliberately NOT modelled on Go's per-file `init#1` counter: Rust's
/// initializer sites are individually addressable named items, not one
/// file-wide sequence, so keying on the item's own (qualified) name is both
/// more precise and trivially unique without a counter.
fn qualified_init_symbol(
    db: &RootDatabase,
    name: &str,
    assoc: Option<AssocItem>,
    display_target: DisplayTarget,
) -> String {
    format!("{}#init", qualify_stem(db, name, assoc, display_target))
}

fn push(
    cx: Ctx<'_, '_>,
    f: ra_ap_hir::Function,
    out: &mut Vec<(model::Function, ra_ap_hir::Function)>,
) {
    let Ctx {
        db,
        sema,
        vfs,
        root,
        target_dir,
    } = cx;
    let Some(src) = f.source(db) else { return };
    let Some(name_node) = src.value.name() else {
        return;
    };

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

    // Family F: a `CrateOrigin::Local` crate does not guarantee its items
    // resolve to files inside the workspace. Every builtin `derive` breaks
    // that assumption — `<LocalType as Debug>::fmt` resolves to `Debug`'s own
    // method declaration in the sysroot (see `is_under_root`) — which leaked
    // an absolute, username-bearing path into the contract AND, because that
    // location is a per-trait CONSTANT shared by every deriving type in the
    // workspace, collided in `main::node_key`: two crates each deriving
    // `Debug` for a `Widget` produced one surviving node and an edge from one
    // crate's caller into the OTHER crate's method. Relocating onto local
    // source repairs the path, the `generated` flag, and the collision
    // together, because the replacement location is per-type rather than
    // per-trait.
    //
    // `relocate_out_of_root` returning `None` leaves the original location in
    // place rather than dropping the node — a drop would be a silent
    // exclusion. `assert_nodes_local` (main.rs) is what stops such a residual
    // from reaching a consumer unnoticed.
    let (file, line, column, relocated) = match is_under_root(vfs, root, file_id) {
        true => (
            repo_relative_path(vfs, root, file_id),
            line + 1,
            column + 1,
            false,
        ),
        false => match relocate_out_of_root(cx, f) {
            Some(loc) => (loc.file, loc.line, loc.column, true),
            None => (
                repo_relative_path(vfs, root, file_id),
                line + 1,
                column + 1,
                false,
            ),
        },
    };

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
    let results = if ret_str == "()" {
        Vec::new()
    } else {
        vec![model::Result_ { ty: ret_str }]
    };
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
            // Already 1-based: both arms of the Family F match above convert
            // from `line_index`'s 0-based line/column, and `model::Loc`
            // (the relocated arm) is 1-based by construction.
            line,
            column,
            kind: if f.self_param(db).is_some() {
                "method"
            } else {
                "func"
            }
            .to_owned(),
            exported: f.visibility(db) == Visibility::Public,
            test,
            // NARROW #[test] signal, unlike `test` above — see
            // model::Function::test_entry for why both are needed.
            test_entry: f.is_test(db),
            root: false, // Task 5
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
            //
            // Family F adds `relocated`: a node that had to be moved back
            // onto local source had no hand-written `fn` of its own to point
            // at (a derive synthesizes the whole method, name token
            // included), which is precisely what `generated` means.
            // `is_macro_kind_synthetic` cannot see these — RA reports the
            // sysroot trait declaration as an ordinary file, so `macro_file()`
            // is `None` and the macro-kind path never fires at all. The
            // `target_dir` test below still keys on the ORIGINAL `file_id`
            // rather than the relocated one; that is intentional and inert,
            // since `relocated` has already forced the field true in every
            // case where the two differ.
            generated: relocated
                || is_macro_kind_synthetic(db, src.file_id)
                || vfs
                    .file_path(file_id)
                    .as_path()
                    .map(|p| p.starts_with(target_dir))
                    .unwrap_or(false),
            // Family D: see push_const's identical comment on this field.
            macro_truncated: false,
            signature: model::Signature { params, results },
            doc,
            trait_impl: trait_impl_loc(cx, f),
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
fn trait_impl_loc(cx: Ctx<'_, '_>, f: ra_ap_hir::Function) -> Option<model::TraitImpl> {
    let Ctx { db, .. } = cx;
    let AssocItemContainer::Impl(imp) = f.as_assoc_item(db)?.container(db) else {
        return None;
    };
    let tr = imp.trait_(db)?; // None => inherent impl, not a trait impl.

    let trait_src = tr.source(db)?;
    let trait_decl = decl_loc(cx, trait_src)?;

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
            if !matches!(
                adt.module(db).krate(db).origin(db),
                CrateOrigin::Local { .. }
            ) {
                model::SelfType::NotEligible
            } else {
                match adt.source(db).and_then(|src| decl_loc(cx, src)) {
                    Some(loc) => model::SelfType::Local {
                        file: loc.file,
                        line: loc.line,
                        column: loc.column,
                    },
                    None => model::SelfType::Unresolved,
                }
            }
        }
    };

    Some(model::TraitImpl {
        trait_decl,
        self_type,
    })
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
fn decl_loc<N: AstNode + HasName>(cx: Ctx<'_, '_>, src: InFile<N>) -> Option<model::Loc> {
    decl_loc_with_file(cx, src).map(|(_, loc)| loc)
}

/// `decl_loc` plus the resolved `FileId` it derived the location from.
/// Split out for Family F: relocating an out-of-root node (see
/// `relocate_out_of_root`) must confirm the *replacement* location is itself
/// inside the workspace, and the repo-relative string `decl_loc` returns
/// cannot answer that — an out-of-root path falls back to an absolute string
/// (see `repo_relative_path`), which is exactly the state being repaired.
/// Testing the `FileId` against the vfs is the same check `is_under_root`
/// applies to the original location, so both sides of the swap are judged by
/// one predicate rather than by string shape.
fn decl_loc_with_file<N: AstNode + HasName>(
    cx: Ctx<'_, '_>,
    src: InFile<N>,
) -> Option<(FileId, model::Loc)> {
    let Ctx {
        db,
        sema,
        vfs,
        root,
        ..
    } = cx;
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
    Some((
        file_id,
        model::Loc {
            file: repo_relative_path(vfs, root, file_id),
            line: line + 1,
            column: column + 1,
        },
    ))
}

/// True when `file_id` is a real file underneath the workspace root — the
/// invariant every emitted node's own `file` must satisfy (Family F).
///
/// A `CrateOrigin::Local` crate is *supposed* to guarantee this, and for
/// hand-written code it does. It does not for a builtin `derive`: RA resolves
/// `<LocalType as Debug>::fmt`'s `source(db)` to the *trait's own method
/// declaration* in the sysroot (`core/src/fmt/mod.rs:1084` is literally
/// `fn fmt(&self, f: &mut Formatter<'_>) -> Result;`), with `macro_file()`
/// returning `None` — so nothing in the macro-kind path even sees it as an
/// expansion. See `relocate_out_of_root` for the repair.
fn is_under_root(vfs: &Vfs, root: &AbsPath, file_id: FileId) -> bool {
    vfs.file_path(file_id)
        .as_path()
        .is_some_and(|p| p.starts_with(root))
}

/// Family F repair: a workspace-local function whose own declaration resolves
/// OUTSIDE the workspace root is relocated onto the local source that caused
/// it to exist, and reported `generated: true`.
///
/// Only builtin `derive`s have been observed producing this state, and for
/// them the honest location is the type declaration carrying the `#[derive]`
/// — that is the line a human edits to remove the impl. Both candidates are
/// tried in specificity order, and each is accepted ONLY if it lands inside
/// the workspace, so the repair can never trade one out-of-root path for
/// another:
///
/// 1. **The `impl` block.** Correct for any hand-written impl whose method
///    somehow resolves out of root, and the more general answer — it exists
///    for `impl Tr for &T` and other shapes where `as_adt()` is `None`.
/// 2. **The self type's own declaration.** The derive case: a derived impl
///    block is synthesized, so (1) is expected to fail or resolve back into
///    the sysroot, while the `Adt` is the user's own `struct`/`enum`.
///
/// Returning `None` means the node is genuinely unexplainable — see the
/// caller for what happens then. Deliberately NOT a silent drop: dropping is
/// an exclusion, and this harness's standing rule is that a divergence gets
/// disclosed, never excluded.
fn relocate_out_of_root(cx: Ctx<'_, '_>, f: ra_ap_hir::Function) -> Option<model::Loc> {
    let Ctx {
        db,
        sema,
        vfs,
        root,
        ..
    } = cx;
    let AssocItemContainer::Impl(imp) = f.as_assoc_item(db)?.container(db) else {
        return None;
    };

    let in_root = |cand: Option<(FileId, model::Loc)>| {
        cand.filter(|(fid, _)| is_under_root(vfs, root, *fid))
            .map(|(_, loc)| loc)
    };

    // (1) the impl block. `ast::Impl` has no name token, so `decl_loc_with_file`
    // (which keys on `HasName`) does not apply; the impl keyword's own span is
    // the analogous anchor.
    let impl_loc = imp.source(db).and_then(|src| {
        let (file_id, line, column) = src_loc(db, sema, src.file_id, src.value.syntax())?;
        Some((
            file_id,
            model::Loc {
                file: repo_relative_path(vfs, root, file_id),
                line: line + 1,
                column: column + 1,
            },
        ))
    });
    if let Some(loc) = in_root(impl_loc) {
        return Some(loc);
    }

    // (2) the self type's declaration — the derive case.
    let adt = imp.self_ty(db).as_adt()?;
    if !matches!(
        adt.module(db).krate(db).origin(db),
        CrateOrigin::Local { .. }
    ) {
        return None;
    }
    in_root(adt.source(db).and_then(|src| decl_loc_with_file(cx, src)))
}

/// The real-file/macro-expansion location handling `push` and
/// `decl_loc_with_file` both apply, over a bare syntax node rather than a
/// named item — needed by `relocate_out_of_root`'s impl-block candidate,
/// since `ast::Impl` carries no name token to key on.
fn src_loc(
    db: &RootDatabase,
    sema: &Semantics<'_, RootDatabase>,
    file_id: HirFileId,
    node: &ra_ap_syntax::SyntaxNode,
) -> Option<(FileId, u32, u32)> {
    match file_id.file_id() {
        Some(efid) => {
            let fid = efid.file_id(db);
            let (line, column) = char_line_col(db, fid, node.text_range().start());
            Some((fid, line, column))
        }
        None => {
            sema.parse_or_expand(file_id);
            let range = sema.original_range(node);
            let fid = range.file_id.file_id(db);
            let (line, column) = char_line_col(db, fid, range.range.start());
            Some((fid, line, column))
        }
    }
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
        None => {
            trimmed
                .lines()
                .next()
                .unwrap_or("")
                .trim_end_matches('.')
                .to_owned()
                + "."
        }
    }
}

/// "crate::module::path" for the function's containing module.
fn module_path(db: &RootDatabase, f: ra_ap_hir::Function) -> String {
    module_path_of(db, f.module(db))
}

/// Same as `module_path`, taking the `Module` directly — `push_const`/
/// `push_static` have no `ra_ap_hir::Function` to call `.module(db)` on.
fn module_path_of(db: &RootDatabase, module: Module) -> String {
    let krate = module
        .krate(db)
        .display_name(db)
        .map(|d| d.to_string())
        .unwrap_or_default();
    let mut parts: Vec<String> = module
        .path_to_root(db)
        .into_iter()
        .rev()
        .filter_map(|m| m.name(db).map(|n| n.as_str().to_owned()))
        .collect();
    parts.insert(0, krate);
    parts.join("::")
}
