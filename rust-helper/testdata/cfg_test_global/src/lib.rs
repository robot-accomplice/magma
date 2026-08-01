//! Family E: `cfg(test)` forced globally. `main.rs`'s `CargoConfig` sets
//! `cfg(test)` on for the whole load (`cfg_overrides.global`) unconditionally
//! — Go's backend deliberately does TWO loads specifically to avoid this.
//! Under that override, `#[cfg(not(test))]` items are not merely
//! misclassified, they never exist at all as far as rust-analyzer's item
//! enumeration is concerned. `real_clock` is therefore absent from the node
//! list outright, and its only callee `clock_helper` has no incoming edge
//! and reads dead — even though a normal, non-test `cargo check` compiles
//! `real_clock` as public API and considers both functions live.
//!
//! `other_root` exists only so this crate has at least one always-visible
//! root and does not hit the helper's separate "no roots in scope" refusal,
//! which would otherwise short-circuit before the family E defect is ever
//! reached.
//!
//! `tests::uses_it` exists only to keep `clock_helper` genuinely live in
//! BOTH of the oracle harness's `--all-targets` compilations (the normal
//! build, where `real_clock` reaches it, and the `cfg(test)` unittest
//! build, where `real_clock` does not exist) — without it, the harness's
//! own `cfg(test)`-enabled compilation unit for `--all-targets` coincidentally
//! ALSO fails to see `real_clock` and independently reports `clock_helper`
//! dead in that build, which makes the divergence "agree" by accident and
//! masks the family entirely. `tests::uses_it` is `test:true` and is excluded
//! from the comparison on both sides by the harness's own normalisation, so
//! it does not itself participate in the FATAL — it only keeps the oracle's
//! verdict on `clock_helper` honestly "live" in both builds, matching a
//! plain (non---all-targets) `cargo check`.
#[cfg(not(test))]
pub fn real_clock() -> i32 {
    clock_helper()
}

fn clock_helper() -> i32 {
    1
}

pub fn other_root() {}

#[cfg(test)]
mod tests {
    use super::clock_helper;

    #[test]
    fn uses_it() {
        clock_helper();
    }
}
