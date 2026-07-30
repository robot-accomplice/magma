//! Family F: nodes inside the user's own rustup toolchain. A bare
//! `#[derive(Debug)]` was reported to make the helper emit a node whose
//! `file` resolves inside `~/.rustup/toolchains/…/library/core/src/fmt/mod.rs`
//! with `generated: false` — Task H changed how `generated` is computed, so
//! this fixture exists to re-verify that claim rather than assume it.
#[derive(Debug)]
pub struct Widget {
    pub id: i32,
}

pub fn make() -> Widget {
    Widget { id: 1 }
}
