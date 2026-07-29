//! Root identification.
//!
//! Rust's root set is NOT Go's. Go refuses when a scope has no non-test `main`,
//! because a library cannot see its external callers. rustc answers differently:
//! it treats a library's `pub` API as live, and magma's derived views must match
//! the compiler set-identically. So a lib crate is analysable, and "dead" for a
//! library means "unreachable from the public API".

use ra_ap_ide_db::RootDatabase;

use crate::model;

/// Mark every production root: a bin target's `main`, or a `pub` item of a
/// library crate. Test and bench functions are NOT production roots — magma
/// walks them separately for the all-roots view.
pub fn mark(db: &RootDatabase, funcs: &mut [(model::Function, ra_ap_hir::Function)]) {
    for (node, f) in funcs.iter_mut() {
        let is_main = f.is_main(db);
        // A `pub` function in a library is externally callable, so it is a root.
        // Test functions are excluded: they are roots for the all-roots walk only.
        let is_public_api = node.exported && !node.test;
        node.root = is_main || is_public_api;
    }
}
