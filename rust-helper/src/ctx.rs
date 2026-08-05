//! The invariant analysis context, bundled once instead of threaded five
//! parameters at a time.
//!
//! `db`, `sema`, `vfs`, `root` and `target_dir` are fixed for an entire run and
//! every enumeration and body-walking helper needs some subset of them.
//! Threading them positionally put five parameters on seventeen functions and
//! forced `#[allow(clippy::too_many_arguments)]` onto five of those — a lint
//! that was correct each time it fired, silenced rather than answered.
//!
//! The pressure was already showing before this struct existed: `walk.rs` grew
//! a `BodyCtx` specifically to stop `walk()` reaching an eleventh positional
//! parameter. That was the right instinct applied to one function; this is the
//! same instinct applied to the path.
//!
//! Deliberately `Copy`: it is five shared references, so passing it costs the
//! same as passing them individually and callers never need to think about
//! borrowing it.

use ra_ap_hir::Semantics;
use ra_ap_ide_db::RootDatabase;
use ra_ap_paths::AbsPath;
use ra_ap_vfs::Vfs;

/// Shared, read-only analysis context for one helper run.
///
/// Two lifetimes rather than one, because they are genuinely different: `'db`
/// is the database and everything interned in it (the HIR types handed back by
/// `sema` borrow from it and outlive individual calls), while `'a` is only the
/// borrow of the surrounding locals. Collapsing them would over-constrain
/// callers that hold the database far longer than the context value.
#[derive(Clone, Copy)]
pub struct Ctx<'a, 'db> {
    pub db: &'db RootDatabase,
    pub sema: &'a Semantics<'db, RootDatabase>,
    pub vfs: &'a Vfs,
    /// The workspace root, canonicalised. Canonicalisation is load-bearing and
    /// not cosmetic: `cargo metadata` reports `target_directory` canonicalised
    /// while rust-analyzer's vfs reports paths as given, and on macOS `/tmp`
    /// resolves to `/private/tmp` — so an uncanonicalised root silently defeats
    /// the `target_dir` prefix comparison below it.
    pub root: &'a AbsPath,
    /// Cargo's target directory, excluded from enumeration. Anchoring the
    /// `generated` flag on this path is the H1 fix; under a symlinked root it
    /// stopped firing for exactly the build-script output it exists to catch.
    pub target_dir: &'a AbsPath,
}
