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

/// Method-reachability case 1: a method on a privately-declared type that IS
/// re-exported. `T` lives in a private `mod`, but `pub use` bridges the TYPE
/// out, so `T::m` is callable externally as `libonly::T::m()` — a method's
/// public-API status is a property of its impl's self type, not the method's
/// own declaring module. rustc's dead_code lint agrees (zero warnings for
/// `m`) — must be root:true.
mod reexported_type {
    pub struct T;
    impl T {
        pub fn m(&self) {}
    }
}
pub use reexported_type::T;

/// Method-reachability case 2: identical shape to `reexported_type::T`, but
/// with NO re-export — `H` and `H::m` are only nameable within this crate.
/// rustc's dead_code lint warns `m` is never used — must be root:false.
mod hidden_type {
    pub struct H;
    impl H {
        pub fn m(&self) {}
    }
}

/// Method-reachability case 3: a public type with a NON-pub method. Even
/// though `U` is reachable, `helper` was never marked `pub`, so it must stay
/// root:false regardless of `U`'s own reachability — over-marking here would
/// invent liveness for something rustc genuinely flags as unused.
pub struct U;
impl U {
    fn helper(&self) {}
}
