//! Family B: desugared calls. `println!("{}", w)` reaches `Widget::fmt`
//! through the format-args machinery (an internal fn-pointer capture, not a
//! syntactic method call), which `walk.rs` never models — it only resolves
//! literal `CallExpr`/`MethodCallExpr` nodes. `walk.rs` also has no handling
//! at all for `ops::*`, `IntoIterator`/`Iterator`, `From`, `Future::poll`, or
//! `Drop` desugaring; format args is the smallest single case to isolate.
//!
//! Deliberately has NO `use` statements anywhere, including at the crate
//! root. `impl core::fmt::Display for Widget` is written fully-qualified —
//! see the plan's trap note: a crate-root `use std::fmt;` would make this
//! impl survive as a root only by accident (roots.rs's re-export-bridge scan
//! would find `fmt::Display` reachable via that unrelated import), which
//! would mask this exact family instead of exposing it.
struct Widget;

impl core::fmt::Display for Widget {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        f.write_str("widget")
    }
}

fn main() {
    let w = Widget;
    println!("{}", w);
}
