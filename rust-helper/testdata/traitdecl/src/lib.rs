//! Task 9 repro: a trait's own *declared* methods (not its impls) must be
//! enumerated, and a default body's calls must be walked for edges.
//!
//! Before the fix, `enumerate::collect` walked only `Module::declarations`
//! (free functions) and `Module::impl_defs` (impl methods) — `Tr::m` itself
//! was never enumerated, so `helper` had no incoming edge and would be
//! reported dead, even though rustc's dead_code lint is silent: it treats
//! `helper` as live via `Tr::m`'s default body regardless of whether `Tr` is
//! ever implemented or `m` ever called.

/// Default-bodied declaration: `m`'s body calls `helper`, which must yield
/// a node for `m` and an edge `Tr::m -> helper`.
///
/// `m2` is a bodyless declaration (required, no default) — it must still
/// appear as a node with zero outgoing edges; it has no body to walk, so
/// omitting edges for it is correct, not a bug.
pub trait Tr {
    fn m(&self) {
        helper();
    }
    fn m2(&self);
}

fn helper() {}

pub struct S;
impl Tr for S {
    fn m2(&self) {}
}
