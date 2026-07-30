//! Family C: calls outside a function body. `walk.rs` only walks
//! `f.source(db).value.body()` — a function's own body — so a call written
//! inside a `const` initializer expression is invisible: the initializer is
//! not itself an enumerated function's body, and no enumerated function's
//! body contains it either.
const fn helper() -> i32 {
    42
}
fn really_dead() -> i32 {
    0
}
const VALUE: i32 = helper();
fn main() {
    println!("{}", VALUE);
}
