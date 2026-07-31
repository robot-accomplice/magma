//! Family B residue: the `?` operator's implicit error conversion.
//!
//! `expr?` desugars to `Try::branch` — which `walk.rs` already resolves via
//! `Semantics::resolve_try_expr` — followed, when the propagated error type
//! differs from the enclosing function's, by an implicit
//! `From::from` through `FromResidual`. That second half has no expression
//! node of its own: it is a hidden argument to a synthesized
//! `FromResidual::from_residual`, so no `Semantics::resolve_*` at the pinned
//! `ra_ap_* = 0.0.343` can reach it.
//!
//! `<HighErr as From<LowErr>>::from` below is therefore reachable ONLY
//! through the `?` in `mid`. rustc does not report it dead; if `walk.rs`
//! cannot see the edge, the helper does — which is a FATAL (false dead code),
//! and is the point of this fixture.
//!
//! Deliberately has NO `use` statements anywhere, including at the crate
//! root, for the same reason `testdata/desugar` does not: a crate-root
//! `use core::convert::From;` would let roots.rs's re-export-bridge scan mark
//! this impl a root by accident and mask the family instead of exposing it.
struct LowErr;

struct HighErr;

impl core::convert::From<LowErr> for HighErr {
    fn from(_e: LowErr) -> HighErr {
        HighErr
    }
}

fn low() -> core::result::Result<i32, LowErr> {
    Ok(1)
}

fn mid() -> core::result::Result<i32, HighErr> {
    let v = low()?;
    Ok(v)
}

fn main() {
    let _ = mid();
}
