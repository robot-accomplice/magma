//! Standard OUT_DIR codegen pattern: write a Rust source file at build time,
//! `include!`d into the crate. Task 11 fixture.
use std::env;
use std::fs;
use std::path::Path;

fn main() {
    let out_dir = env::var("OUT_DIR").unwrap();
    let dest = Path::new(&out_dir).join("generated.rs");
    fs::write(&dest, "pub fn generated_fn() { production_callee(); }\n").unwrap();
}
