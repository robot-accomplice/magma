//! Root identification.
//!
//! Rust's root set is NOT Go's. Go refuses when a scope has no non-test `main`,
//! because a library cannot see its external callers. rustc answers differently:
//! it treats a library's public API as live, and magma's derived views must match
//! the compiler set-identically. So a lib crate is analysable, and "dead" for a
//! library means "unreachable from the public API".
//!
//! "Public API" means *reachable from the crate root via a chain of public
//! bindings* — not just an item's own `pub` keyword, and not just "every
//! ancestor module is `pub`":
//!   - A `pub fn` nested in a private (non-`pub`) `mod` is not nameable from
//!     outside the crate — UNLESS a `pub use` bridges it back out. rustc's
//!     dead_code lint agrees either way.
//!   - So an all-ancestors-`pub` walk (`all_ancestors_public`) is sound (never
//!     over-marks) but incomplete: it misses exactly that re-export-bridge
//!     case, which is the dangerous direction to get wrong (it invents false
//!     dead code for live public API).
//!   - A method's public-API status is not a property of the *method's own*
//!     module chain at all — for an inherent impl it's a property of the
//!     impl's self type; for a trait impl it's a property of the trait alone.
//!     A privately-declared type re-exported via `pub use` makes every public
//!     inherent method on it live, even though the method's own declaring
//!     module is still private. `is_public_method` answers this by testing
//!     the self type and/or trait against the same reachable set
//!     `public_reachable` already computes for free items — types and traits
//!     show up in `Module::scope` exactly like functions and modules do.
//!   - A *trait-impl* method's own `Visibility` is worthless as a signal: Rust
//!     forbids `pub` on it syntactically, so it is never `Visibility::Public`
//!     no matter how public the API is. Its reachability comes from the trait
//!     alone — not the self type's shape, which the orphan rule means can
//!     only be non-`Adt` (a builtin, tuple, or reference) inside a trait impl
//!     in the first place — see `is_public_method`.
//!
//! `public_reachable` walks `Module::scope`, which surfaces `use`-imported
//! names alongside declared ones — so a `pub use` re-export of a privately-
//! nested item (function, type, or trait) shows up directly at the reachable
//! module, without needing to descend into the private module at all.

use std::collections::{HashMap, HashSet, VecDeque};

use ra_ap_hir::{AsAssocItem, AssocItemContainer, Crate, HasVisibility, ModuleDef, ScopeDef, Visibility};
use ra_ap_ide_db::RootDatabase;

use crate::model;

/// Mark every production root: a bin target's `main`, or an item reachable
/// from the crate root of a library crate through public bindings only. Test
/// functions are NEVER production roots — magma walks them separately for the
/// all-roots view — even a `#[test] fn main`.
pub fn mark(db: &RootDatabase, funcs: &mut [(model::Function, ra_ap_hir::Function)]) {
    // BFS is per-crate; cache it since a workspace's functions share crates.
    let mut public_by_crate: HashMap<Crate, HashSet<ModuleDef>> = HashMap::new();
    for (node, f) in funcs.iter_mut() {
        if node.test {
            node.root = false;
            continue;
        }
        let krate = f.module(db).krate(db);
        let public =
            public_by_crate.entry(krate).or_insert_with(|| public_reachable(db, krate));
        node.root = f.is_main(db)
            || public.contains(&ModuleDef::Function(*f))
            || all_ancestors_public(db, *f)
            || is_public_method(db, *f, public);
    }
}

/// BFS from the crate root through `Module::scope(db, None)` (unfiltered: this
/// surfaces both items declared in a module AND items brought into it via
/// `use`/`pub use`, which is exactly what lets a re-export bridge a privately
/// nested item back out). At each entry, `ModuleDef::visibility(db)` — the
/// item's own fixed, declaration-site visibility, a plain data comparison,
/// not a from-module query — decides inclusion: a bare `pub` binding passes,
/// `pub(crate)`/private does not. Recursion only descends into modules whose
/// own visibility is `Public`, so a private `mod` is never expanded (its
/// un-re-exported contents are correctly never discovered) — but a
/// `pub use private_mod::item;` sitting in an already-reachable module still
/// surfaces `item` directly, without ever needing to enter `private_mod`.
///
/// Collects every kind of publicly reachable `ModuleDef`, not just functions:
/// types (`Adt`) and traits are needed too, so `is_public_method` can test an
/// `impl`'s self type and trait against this same set.
///
/// Known gap: this cannot distinguish a `pub use` from a plain `use` of the
/// same target — both bring the name into the reachable module's scope, and
/// both pass this check if the target's own declaration is `pub`. A plain
/// `use private_mod::pub_fn;` (imported for internal use, not re-exported)
/// would therefore be over-marked as a root. This is the safe direction (it
/// can miss reporting some dead code, but never invents false dead code) and
/// is not exercised by any fixture in this repo; distinguishing the two needs
/// per-binding import visibility, which is not reachable through `ra_ap_hir`'s
/// public API without depending on `ra_ap_hir_def` internals directly.
fn public_reachable(db: &RootDatabase, krate: Crate) -> HashSet<ModuleDef> {
    let mut found = HashSet::new();
    let mut visited = HashSet::new();
    let mut queue = VecDeque::from([krate.root_module(db)]);
    while let Some(m) = queue.pop_front() {
        if !visited.insert(m) {
            continue;
        }
        for (_, def) in m.scope(db, None) {
            let ScopeDef::ModuleDef(md) = def else { continue };
            if md.visibility(db) != Visibility::Public {
                continue;
            }
            if let ModuleDef::Module(sub) = md {
                queue.push_back(sub);
            }
            found.insert(md);
        }
    }
    found
}

/// Sound but incomplete fallback: true only if the item's own visibility and
/// every module from it up to the crate root are declared `pub`. Whenever
/// this is true, a direct `crate::a::b::item` path exists, so it never
/// over-marks — but it misses re-export bridges, which `public_reachable`
/// handles for free items (and this fallback is redundant for them: anything
/// it finds true, the BFS above finds true too, by construction). For
/// associated items (methods), this is only correct when the method's own
/// declaring module chain is genuinely all-`pub` with no re-export involved —
/// `is_public_method` covers the re-export case; this stays as a fallback for
/// shapes `is_public_method` can't resolve (e.g. an impl on a builtin/
/// primitive type, which has no `Adt` to look up).
fn all_ancestors_public(db: &RootDatabase, f: ra_ap_hir::Function) -> bool {
    f.visibility(db) == Visibility::Public
        && f.module(db).path_to_root(db).into_iter().all(|m| m.visibility(db) == Visibility::Public)
}

/// A method's public-API status is a property of its `impl` (or, for a trait
/// declaration's own method, its `trait`) — not its own declaring module.
///
///   - **Trait declaration** (container is `AssocItemContainer::Trait`): a
///     trait's declared method — default-bodied or not — is nameable
///     (`Tr::m(&x)`) and, if default-bodied, is the function every
///     non-overriding impl falls back to. So its own reachability tracks the
///     trait's: if a caller can name `Tr`, it can reach `Tr::m`. This mirrors
///     the trait-impl arm below but needs no self-type check — there is no
///     `impl` here, just the trait itself. Gating on `f.visibility(db)` alone
///     would miss the re-export-bridge case (`pub trait Tr {..}` declared in a
///     private module, re-exported via `pub use`) exactly as it would for a
///     free function, which is why this checks `public` (built from
///     `Module::scope`, which surfaces re-exports) rather than stopping at
///     `all_ancestors_public`.
///   - **Impl** (container is `AssocItemContainer::Impl`): diverges immediately
///     on `Impl::trait_` for where the *method's* own reachability comes from
///     — trait impls and inherent impls are gated on different things, not a
///     shared self-type precondition:
///     - **Trait impl** (`Impl::trait_` is `Some`): from the trait alone, no
///       self-type check at all. A method in `impl Tr for S` can NEVER
///       syntactically carry `pub` — Rust rejects the keyword there — so its
///       `Visibility` is whatever the impl item's own AST yields, which is
///       never `Public`. (`ra_ap_hir_def`'s `assoc_visibility` substitutes the
///       trait's visibility only for the *declaration* inside `trait Tr { .. }`,
///       whose container is `ItemContainerId::TraitId`; the impl block's copy
///       has container `ItemContainerId::ImplId` and falls through to
///       `visibility_from_ast`.) Gating on the method's own `pub` would
///       therefore mark every trait-impl method in every crate dead —
///       `Display`, `Iterator`, every custom trait — so the gate here is the
///       trait's reachability instead: if a caller can name both `Tr` and `S`,
///       it can call `S::m`, and rustc's dead_code lint agrees (it is silent for
///       `pub trait Tr { fn m(&self); } pub struct S; impl Tr for S { fn m(&self){} }`).
///     - **Inherent impl** (`trait_` is `None`): requires the impl's self type
///       to resolve to an `Adt` in the reachable set, AND the method's own
///       `pub` — which it CAN carry and which rustc does enforce, a non-`pub`
///       method on a public type is flagged unused.
///
/// For a **trait impl**, the self type's shape never gates root status: the
/// orphan rule means a non-`Adt` self type (a builtin, tuple, or reference)
/// can only ever appear in a trait impl, never an inherent one, so there is
/// no "is the self type itself public" question to ask independently of the
/// trait — `impl Tr2 for i32 { fn m2(&self) {} }` is public API whenever
/// `Tr2` is, regardless of `i32` having no `Adt` to look up.
/// `cargo check` agrees: silent for `pub trait Tr2 { fn m2(&self); } impl
/// Tr2 for i32 { fn m2(&self) {} }`.
///
/// For an **inherent impl**, the self type must still resolve to an `Adt`
/// (struct/enum/union) and be in the reachable set; impls on other type
/// shapes fall through to `all_ancestors_public` instead (always false for a
/// method, since methods never appear in `Module::scope`, so this is a
/// no-op fallback for the trait-impl arm above, not a regression).
fn is_public_method(db: &RootDatabase, f: ra_ap_hir::Function, public: &HashSet<ModuleDef>) -> bool {
    let Some(assoc) = f.as_assoc_item(db) else { return false };
    match assoc.container(db) {
        AssocItemContainer::Trait(tr) => public.contains(&ModuleDef::Trait(tr)),
        AssocItemContainer::Impl(imp) => match imp.trait_(db) {
            Some(tr) => public.contains(&ModuleDef::Trait(tr)),
            None => {
                let Some(adt) = imp.self_ty(db).as_adt() else { return false };
                public.contains(&ModuleDef::Adt(adt)) && f.visibility(db) == Visibility::Public
            }
        },
    }
}
