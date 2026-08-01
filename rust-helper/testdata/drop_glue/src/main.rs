//! Family B residue: `Drop::drop` at scope exit.
//!
//! Dropping a value calls `Drop::drop`, but scope exit produces no expression
//! node at all — there is nothing for `walk.rs` to cast to an `ast::` shape or
//! hand to a `Semantics::resolve_*`. So `<Guard as Drop>::drop` below has no
//! incoming edge, and neither does `cleanup`, which only `drop` calls.
//!
//! WHERE THE ORACLE ACTUALLY DISAGREES. rustc's `dead_code` lint does not
//! report trait-impl methods at all, so it never names `drop` — measured on a
//! probe crate, it says `struct NeverConstructed is never constructed` and
//! `function cleanup_never is never used`, and stays silent about the `drop`
//! between them. The observable symptom is therefore one hop PAST `drop`, on
//! its transitive callee: `cleanup` is live for rustc and dead for the
//! helper. That is this fixture's FATAL.
//!
//! Deliberately does NOT include a never-constructed `Drop` type. That shape
//! produces a FATAL no edge fix can close — rustc's silence about trait-impl
//! methods means a genuinely-dead one always reads "live" to this harness —
//! and it is a general property of trait impls, not specific to `Drop`, so
//! folding it in here would make this fixture untestable for its own family.
//!
//! No `use` statements anywhere, including the crate root — same reason as
//! `testdata/desugar` and `testdata/try_from`.
//! Two shapes, because the direct one alone does not pin the requirement.
struct Guard;

impl core::ops::Drop for Guard {
    fn drop(&mut self) {
        cleanup();
    }
}

fn cleanup() {}

// TRANSITIVE drop glue. `Inner` is never written as an expression ANYWHERE in
// this program: it comes into being only through `Outer`'s derived `Default`,
// which calls `Inner`'s derived `Default`. So a fix that only maps an
// occurring type to its own `Drop` impl still calls `inner_cleanup` dead —
// while it genuinely runs, and rustc agrees it is live. Both derives are
// load-bearing; writing either `Default` by hand would put an `Inner`
// expression in the source and silently defeat this half of the fixture.
#[derive(Default)]
struct Inner;

impl core::ops::Drop for Inner {
    fn drop(&mut self) {
        inner_cleanup();
    }
}

fn inner_cleanup() {}

#[derive(Default)]
struct Outer {
    inner: Inner,
}

fn main() {
    let _g = Guard;
    let _o = Outer::default();
}
