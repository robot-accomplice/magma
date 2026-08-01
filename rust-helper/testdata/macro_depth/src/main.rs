//! Family D: macro recursion depth guard truncates silently. `walk.rs`'s
//! macro descent gives up after depth 8 (`if depth > 8 { return; }`) with no
//! disclosure. A hand-written recursive tt-muncher recursing more than 8
//! macro-expansion levels deep before reaching its base-case call
//! demonstrates the truncation without any third-party dependency —
//! `serde_json::json!` would also trigger it (at just the 2nd key) but at
//! the cost of a network dependency this fixture avoids.
macro_rules! count_down {
    () => {
        base_case();
    };
    ($_head:tt $($tail:tt)*) => {
        count_down!($($tail)*);
    };
}

fn base_case() {}

fn main() {
    count_down!(a b c d e f g h i j);
}
