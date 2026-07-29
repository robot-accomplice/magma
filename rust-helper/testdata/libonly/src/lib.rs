//! A library crate with no `main` — the shape most Rust crates take.
pub fn public_api() { internal_helper(); }
fn internal_helper() {}
fn truly_dead() {}
