// `#[path = ...]` cannot take `concat!(env!(...))`, so `include!` is
// effectively the only mechanism for OUT_DIR codegen (Task 11).
include!(concat!(env!("OUT_DIR"), "/generated.rs"));

fn main() {
    generated_fn();
}

fn production_callee() {}
