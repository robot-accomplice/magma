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
//!
//! `public_reachable` fixes that by walking `Module::scope`, which surfaces
//! `use`-imported names alongside declared ones — so a `pub use` re-export of
//! a privately-nested item shows up directly at the reachable module, without
//! needing to descend into the private module at all.

use std::collections::{HashMap, HashSet, VecDeque};

use ra_ap_hir::{Crate, HasVisibility, ModuleDef, ScopeDef, Visibility};
use ra_ap_ide_db::RootDatabase;

use crate::model;

/// Mark every production root: a bin target's `main`, or an item reachable
/// from the crate root of a library crate through public bindings only. Test
/// functions are NEVER production roots — magma walks them separately for the
/// all-roots view — even a `#[test] fn main`.
pub fn mark(db: &RootDatabase, funcs: &mut [(model::Function, ra_ap_hir::Function)]) {
    // BFS is per-crate; cache it since a workspace's functions share crates.
    let mut public_by_crate: HashMap<Crate, HashSet<ra_ap_hir::Function>> = HashMap::new();
    for (node, f) in funcs.iter_mut() {
        if node.test {
            node.root = false;
            continue;
        }
        let krate = f.module(db).krate(db);
        let public =
            public_by_crate.entry(krate).or_insert_with(|| public_reachable(db, krate));
        node.root = f.is_main(db) || public.contains(f) || all_ancestors_public(db, *f);
    }
}

/// BFS from the crate root through `Module::scope(db, None)` (unfiltered: this
/// surfaces both items declared in a module AND items brought into it via
/// `use`/`pub use`, which is exactly what lets a re-export bridge a privately
/// nested item back out). At each entry, `Function`/`Module::visibility(db)`
/// — the item's own fixed, declaration-site visibility, a plain data
/// comparison, not a from-module query — decides inclusion: a bare `pub fn`
/// or `pub mod` passes, `pub(crate)`/private does not. Recursion only
/// descends into modules whose own visibility is `Public`, so a private `mod`
/// is never expanded (its un-re-exported contents are correctly never
/// discovered) — but a `pub use private_mod::item;` sitting in an already-
/// reachable module still surfaces `item` directly, without ever needing to
/// enter `private_mod`.
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
fn public_reachable(db: &RootDatabase, krate: Crate) -> HashSet<ra_ap_hir::Function> {
    let mut found = HashSet::new();
    let mut visited = HashSet::new();
    let mut queue = VecDeque::from([krate.root_module(db)]);
    while let Some(m) = queue.pop_front() {
        if !visited.insert(m) {
            continue;
        }
        for (_, def) in m.scope(db, None) {
            match def {
                ScopeDef::ModuleDef(ModuleDef::Function(f))
                    if f.visibility(db) == Visibility::Public =>
                {
                    found.insert(f);
                }
                ScopeDef::ModuleDef(ModuleDef::Module(sub))
                    if sub.visibility(db) == Visibility::Public =>
                {
                    queue.push_back(sub);
                }
                _ => {}
            }
        }
    }
    found
}

/// Sound but incomplete fallback: true only if the item's own visibility and
/// every module from it up to the crate root are declared `pub`. Whenever
/// this is true, a direct `crate::a::b::item` path exists, so it never
/// over-marks — but it misses re-export bridges, which `public_reachable`
/// handles for free items (and this fallback is redundant for them: anything
/// it finds true, the BFS above finds true too, by construction). Associated
/// items (methods) never appear in `Module::scope` — they're reached via
/// `Type::method`, not `module::name` — so this is the only signal available
/// for them; a method whose type is re-exported from a private module is a
/// known, undetected gap here.
fn all_ancestors_public(db: &RootDatabase, f: ra_ap_hir::Function) -> bool {
    f.visibility(db) == Visibility::Public
        && f.module(db).path_to_root(db).into_iter().all(|m| m.visibility(db) == Visibility::Public)
}
