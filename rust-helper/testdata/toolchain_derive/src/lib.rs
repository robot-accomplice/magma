//! Family F: nodes resolving inside the user's own rustup toolchain.
//!
//! A bare `#[derive(Debug)]` used to make the helper emit a node whose `file`
//! resolved inside `~/.rustup/toolchains/…/library/core/src/fmt/mod.rs` with
//! `generated: false`. Both are now repaired — the node lands on the `Widget`
//! name token below, `generated: true` — by
//! `enumerate::relocate_out_of_root`. See `oracle-expected.json` for the
//! measured root cause and for what remains open (`root`/`exported` still
//! over-report, in the safe direction).
//!
//! This fixture is NOT reachability coverage: the derived method is always
//! marked reachable, so the helper and rustc can never disagree about it and
//! `fatal` is empty here by construction. The actual guard is
//! `main::non_local_paths`, which refuses the run outright if any emitted
//! path escapes the workspace root.
#[derive(Debug)]
pub struct Widget {
    pub id: i32,
}

pub fn make() -> Widget {
    Widget { id: 1 }
}
