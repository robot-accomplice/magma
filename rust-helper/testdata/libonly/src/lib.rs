//! A library crate with no `main` — the shape most Rust crates take.
pub fn public_api() { internal_helper(); }
fn internal_helper() {}
fn truly_dead() {}

/// A private module: items inside are not nameable from outside the crate,
/// even when individually marked `pub`. rustc's dead_code lint still flags
/// `pub_in_private_mod` as unused, so it must NOT be a root.
mod internal {
    pub fn pub_in_private_mod() {
        helper();
    }
    fn helper() {}
}

// A `#[test]` function that happens to be named `main`. `Function::is_main`
// classifies this as a bin's `main` on name + crate-root-module grounds alone
// — it must NOT become a root, because it is also a test, and tests are
// walked separately for the all-roots view, never as production roots.
#[test]
fn main() {
    assert_eq!(2 + 2, 4);
}

/// A `pub fn` in a private module, re-exported at the crate root. Unlike
/// `internal::pub_in_private_mod` above (never re-exported, correctly NOT a
/// root), rustc's dead_code lint reports zero warnings for `thing` — the
/// `pub use` bridges it out, so it IS live public API and must be a root.
mod facade {
    pub fn thing() {}
}
pub use facade::thing;
