//! Root identification.
//!
//! Rust's root set is NOT Go's. Go refuses when a scope has no non-test `main`,
//! because a library cannot see its external callers. rustc answers differently:
//! it treats a library's `pub` API as live, and magma's derived views must match
//! the compiler set-identically. So a lib crate is analysable, and "dead" for a
//! library means "unreachable from the public API".
//!
//! "Public API" means *effective* visibility, not the item's own `pub` keyword.
//! A `pub fn` nested inside a private (non-`pub`) module is not nameable from
//! outside the crate — rustc's dead_code lint flags it — so root status walks
//! the module chain up to the crate root and requires every module on that
//! path to be `pub` too. `model::Function::exported` (magma's DTO field) stays
//! the item's syntactic `pub` keyword; `root` here is the stricter, oracle-
//! matching computation.

use ra_ap_hir::{HasVisibility, Visibility};
use ra_ap_ide_db::RootDatabase;

use crate::model;

/// Mark every production root: a bin target's `main`, or an effectively-public
/// item of a library crate. Test functions are NEVER production roots — magma
/// walks them separately for the all-roots view — even a `#[test] fn main`.
pub fn mark(db: &RootDatabase, funcs: &mut [(model::Function, ra_ap_hir::Function)]) {
    for (node, f) in funcs.iter_mut() {
        node.root = !node.test && (f.is_main(db) || is_public_api(db, *f));
    }
}

/// An item is part of the effective public API only if it, and every module on
/// the path from its containing module up to the crate root, are declared
/// `pub`. A private `mod` blocks the path even when the item inside it is
/// itself marked `pub`, because there is no public name by which an external
/// crate could reach it (absent a re-export, which this does not chase).
fn is_public_api(db: &RootDatabase, f: ra_ap_hir::Function) -> bool {
    f.visibility(db) == Visibility::Public
        && f.module(db).path_to_root(db).into_iter().all(|m| m.visibility(db) == Visibility::Public)
}
