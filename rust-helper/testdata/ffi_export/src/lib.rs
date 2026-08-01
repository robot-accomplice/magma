//! Family G: symbols exported to a foreign caller.
//!
//! `#[no_mangle]` and `#[export_name]` make a function externally callable
//! whether or not it is `pub`: the compiler emits the symbol, and rustc's own
//! `dead_code` lint treats such items — and everything they reach — as LIVE.
//! Nothing inside the Rust graph calls them, exactly as nothing calls `main`.
//!
//! `roots.rs` knew about a bin's `main` and about publicly reachable items,
//! but not about exported symbols, so these read as dead. That is false dead
//! code on the canonical shape of every Rust library exposed over FFI: a
//! cdylib for C/Python/Node, a staticlib, a WASM export, an embedded
//! interrupt handler.
//!
//! `ordinary_api` exists so this crate has a public API and therefore does NOT
//! take the "no roots in scope" refusal — without it the defect hides behind a
//! refusal instead of surfacing as the FATALs it really is.
//!
//! `truly_dead` is the control: genuinely dead, and rustc agrees. It must stay
//! reported, which is what stops the fix from being "mark everything a root".
pub fn ordinary_api() -> i32 {
    1
}

#[no_mangle]
extern "C" fn exported_no_mangle() -> i32 {
    reached_only_via_no_mangle()
}

fn reached_only_via_no_mangle() -> i32 {
    7
}

#[export_name = "renamed_symbol"]
extern "C" fn exported_under_another_name() -> i32 {
    reached_only_via_export_name()
}

fn reached_only_via_export_name() -> i32 {
    9
}

fn truly_dead() -> i32 {
    0
}
