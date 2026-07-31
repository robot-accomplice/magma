//! The JSON contract between the helper and magma. The helper is an extractor:
//! it emits nodes and edges, never reachability or views — magma derives those.

use serde::Serialize;

/// Wire-format version of the helper's output. Bump on any breaking change.
pub const CONTRACT_VERSION: &str = "magma-rust-helper/1";

#[derive(Serialize)]
pub struct Output {
    pub contract_version: String,
    /// Whether producing this output ran the analysed repository's own code:
    /// `build.rs` scripts executed and proc macros expanded. Go analysis never
    /// does this — it only type-checks — so this is a trust-boundary fact a
    /// consumer should be able to read rather than infer from `language ==
    /// "rust"`. Derived from the `LoadCargoConfig` actually used to load the
    /// workspace (see `main.rs`), not hard-coded, so it stays honest if a
    /// sandboxed or no-execution mode is ever added.
    pub executed_target_code: bool,
    pub functions: Vec<Function>,
    pub calls: Vec<Call>,
}

impl Output {
    pub fn new(executed_target_code: bool, functions: Vec<Function>, calls: Vec<Call>) -> Self {
        Output {
            contract_version: CONTRACT_VERSION.to_owned(),
            executed_target_code,
            functions,
            calls,
        }
    }
}

/// One function or method. `id` is assigned by enumeration order and is the
/// ONLY way edges identify endpoints — names are ambiguous (two impls of one
/// trait share a method name).
#[derive(Serialize)]
pub struct Function {
    pub id: u32,
    /// Bare for a free function; qualified with its receiver/impl type for
    /// an associated item (`T::m`, or `<T as Trait>::m` for a trait impl —
    /// see `enumerate::qualified_symbol`) so `(pkg, symbol)` is unique. `id`
    /// remains the only field edges use to identify endpoints; `symbol` is
    /// for display and for keying a consumer's own node id.
    pub symbol: String,
    /// Crate + module path, e.g. "mycrate::net::client".
    pub pkg: String,
    /// Repo-relative path.
    pub file: String,
    pub line: u32,
    /// 1-based **character** column of the function's own name token, matching
    /// rustc's `column_start` convention in its JSON diagnostics. Note this is a
    /// character count (Unicode scalar values, via `WideEncoding::Utf32`), NOT
    /// ra_ap's native UTF-8 byte offset — the two diverge on any line containing
    /// multi-byte text, and rustc counts characters. See `enumerate::char_line_col`.
    /// (verified directly: a `trait \`DeadTr\` is never used` diagnostic for `pub trait
    /// DeadTr` at 4-space indent reports `column_start: 15` — 4 spaces + the
    /// 10-char `"pub trait "` prefix + 1 for 1-based = 15). Exists so the
    /// oracle-diff harness can key a dead_code diagnostic's primary span
    /// against a specific declaration by (file, line, column), not just
    /// (file, line) — two distinct declarations legitimately sharing one
    /// source line (e.g. `trait Tr { fn a(&self); fn b(&self); }` written on
    /// a single line) are otherwise indistinguishable by line alone, and
    /// rustc's own diagnostic column for such a case correctly points at the
    /// specific method name, not just the shared line.
    pub column: u32,
    /// "func" for free functions AND associated fns without a receiver;
    /// "method" for anything with a `self` receiver; "init" for a
    /// synthesized node standing in for a const/static/associated-const
    /// item's initializer expression (Family C — see
    /// `enumerate::collect_inits`). A new VALUE on this existing field, not
    /// a new field, so per the recorded contract-governance rule (new value
    /// = one-sided, new field = two-sided) this needs no consumer
    /// coordination before shipping. An "init" node has no `fn`/`method`
    /// declaration of its own to point `file`/`line`/`column` at — they
    /// point at the const/static item's own name token instead.
    pub kind: String,
    /// `pub` visibility, syntactic: the item's own `pub` keyword, independent
    /// of whether an enclosing module is private. Root status (see `root`) is
    /// computed separately in `roots.rs` from *effective* visibility, because
    /// a `pub fn` in a private module is not part of the crate's public API.
    pub exported: bool,
    /// Genuinely test-only: never reachable in a production (non-test)
    /// build. True for `#[test]`, but also for anything under `#[cfg(test)]`
    /// ancestry or inside a Cargo `tests/`/`benches/` integration target —
    /// see `enumerate::is_test_context`, which computes this; it is NOT
    /// simply the `#[test]` attribute.
    pub test: bool,
    /// Carries a literal `#[test]` attribute (`Function::is_test`) — the
    /// NARROW signal, and the test half of any all-roots reachability seed.
    ///
    /// Distinct from `test` above, which is deliberately broader, and the two
    /// are not interchangeable for this purpose: seeding a BFS on the broad
    /// flag makes every function merely living under `#[cfg(test)]` ancestry
    /// its own root, so it trivially "reaches" itself and a genuinely dead
    /// test helper can never be reported. Seeding on `root` alone instead
    /// loses test reachability entirely, since `roots::mark` sets `root` for
    /// PRODUCTION entry points only.
    ///
    /// Exists because a consumer of this contract has no third option. The
    /// oracle harness had already hit this and worked around it by regex-
    /// scanning source text for `#[test]` in `scripts/oracle-diff.sh` — a
    /// consumer reading only the JSON cannot do that, so the signal belongs
    /// on the wire. `bench` next to it was always the narrow attribute
    /// signal; this makes the pair symmetric.
    ///
    /// A new FIELD on `magma-rust-helper/1`, which is the helper->magma
    /// contract and internal. It is NOT on the Architext-facing
    /// `magma-code-graph/1` (whose root is `additionalProperties: false`), so
    /// it needs no coordination — see the mapping boundary in
    /// `internal/backend/rust.go`, which strips it.
    pub test_entry: bool,
    /// An entry point: a bin `main`, or a lib `pub` item. Set in Task 5.
    pub root: bool,
    /// Benchmark function (`Function::is_bench`) — counts toward all-roots only.
    pub bench: bool,
    /// Build-script- or macro-generated code.
    pub generated: bool,
    /// Family D disclosure: set when `walk.rs`'s macro-expansion-depth guard
    /// (`walk::MACRO_DEPTH_LIMIT`) cut off descent somewhere while walking
    /// this function's own body (or, for a synthesized "init" node — Family
    /// C — its initializer expression). A truncated expansion is
    /// indistinguishable from one containing no further calls, so THIS
    /// FUNCTION'S OWN outgoing-edge set is incomplete when this is `true` —
    /// any callee past the guard is invisible, and cannot be told apart from
    /// one that was never there. This is per-function, not a single global
    /// "something somewhere was truncated" flag, so a consumer can withhold
    /// judgement on exactly the functions affected rather than the whole
    /// graph: a dead-code verdict for a function with `macro_truncated:
    /// true` rests on incomplete evidence and should not be reported without
    /// disclosing that. Never used by the helper itself to exclude or
    /// suppress anything (§Non-negotiable) — it only discloses the fact.
    pub macro_truncated: bool,
    pub signature: Signature,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub doc: Option<String>,
    /// Set only when this function is an assoc item of a *trait* impl
    /// (`impl Trait for Type { .. }`) — never for free functions, inherent-
    /// impl items, or a trait declaration's own method. Lets the oracle-diff
    /// harness key its dead-code cascade-suppression check on declaration
    /// (file, line, column) instead of a bare, collision-prone name (see
    /// `scripts/oracle-diff.sh`/`.jq`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub trait_impl: Option<TraitImpl>,
}

/// (file, line, column) locations the oracle-diff harness needs to test
/// rustc's trait-impl cascade-suppression: rustc silences a per-method
/// dead_code diagnostic when the enclosing trait/self-type cluster is itself
/// dead.
#[derive(Serialize)]
pub struct TraitImpl {
    /// Declaration site of the implemented trait.
    pub trait_decl: Loc,
    pub self_type: SelfType,
}

/// Three states, deliberately not collapsed to `Option<Loc>`: `NotEligible`
/// and `Unresolved` both mean "no location", but they must NOT be treated
/// the same by a consumer testing "self type is dead OR not independently
/// diagnosable" — `NotEligible` is allowed to satisfy that OR, `Unresolved`
/// must not (an exclusion must never rest on a failed lookup; a disclosed
/// FATAL is safer). Collapsing them to one `None` would let a resolution
/// *failure* silently license an exclusion the same way a genuine builtin
/// self type does.
#[derive(Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum SelfType {
    /// A workspace-local struct/enum/union whose own declaration was found.
    Local {
        file: String,
        line: u32,
        column: u32,
    },
    /// A builtin, tuple, reference, or externally-defined (non-workspace-
    /// local) type — including a generic instantiation of an external type
    /// (e.g. `impl Tr for Vec<S>`, where `as_adt()` resolves to `Vec` itself,
    /// not the argument `S`, so it's `Vec`'s own external origin that lands
    /// here even when `S` is workspace-local). See `enumerate.rs::trait_impl_loc`
    /// for the two distinct paths that both produce this variant.
    NotEligible,
    /// The self type resolved to a workspace-local `Adt`, but its own
    /// declaration's (file, line, column) could not be determined (source or
    /// name lookup failed). Never observed in practice as of Task 13's fix, but
    /// modelled explicitly so the harness can fail safe rather than treat an
    /// unresolved lookup as license to exclude.
    Unresolved,
}

#[derive(Serialize)]
pub struct Loc {
    pub file: String,
    pub line: u32,
    pub column: u32,
}

#[derive(Serialize)]
pub struct Signature {
    pub params: Vec<Param>,
    pub results: Vec<Result_>,
}

#[derive(Serialize)]
pub struct Param {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    pub ty: String,
}

#[derive(Serialize)]
pub struct Result_ {
    pub ty: String,
}

/// One call edge. Endpoints are Function ids.
#[derive(Serialize)]
pub struct Call {
    pub from: u32,
    pub to: u32,
    /// Repo-relative path of the call site.
    pub site_file: String,
    pub site_line: u32,
    /// "static" when the target is resolved unambiguously; "dynamic" when the
    /// call goes through `dyn Trait` and the concrete impl is chosen at runtime.
    pub kind: String,
}

/// Emitted instead of Output when analysis cannot proceed. magma turns this
/// into a refused graph — never a partial or degraded map.
#[derive(Serialize)]
pub struct Refusal {
    pub contract_version: String,
    /// Whether producing THIS refusal ran the analysed repository's own
    /// code, same meaning as `Output::executed_target_code` — NOT always
    /// `false`. A refusal can fire after a successful workspace load (e.g.
    /// "no roots in scope"), by which point build scripts already executed
    /// and the proc-macro server already expanded macros; reporting `false`
    /// there would be a lie on the one field whose entire purpose is to be a
    /// trust boundary. Only refusals that fire BEFORE `load_workspace_at` is
    /// even attempted (a missing argument) or that come FROM its own failure
    /// (not a valid cargo workspace — which fails before build-script
    /// execution) are honestly `false`. Callers of `Refusal::new` must pass
    /// the real value for the point where the refusal fires, not a constant.
    pub executed_target_code: bool,
    pub computable: bool,
    pub reason: String,
}

impl Refusal {
    pub fn new(executed_target_code: bool, reason: impl Into<String>) -> Self {
        Refusal {
            contract_version: CONTRACT_VERSION.to_owned(),
            executed_target_code,
            computable: false,
            reason: reason.into(),
        }
    }
}
