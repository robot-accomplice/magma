//! Family A: function used as a VALUE, never called directly.
//! `walk.rs` only resolves `CallExpr` with a `PathExpr` callee — `.map(f)`, a
//! static fn-pointer table entry, and a plain `let g = f;` binding all pass a
//! function's *name* as a value, so none of them produce a CallExpr and none
//! of them produce an edge.
fn double(x: i32) -> i32 {
    x * 2
}
fn handler_a() -> i32 {
    1
}
const fn init_b_const() -> i32 {
    9
}
fn really_dead() -> i32 {
    0
}
static TABLE: [fn() -> i32; 1] = [handler_a];
static S: i32 = init_b_const();
fn main() {
    let v: Vec<i32> = vec![1, 2, 3].into_iter().map(double).collect();
    println!("{:?} {} {}", v, TABLE[0](), S);
}
