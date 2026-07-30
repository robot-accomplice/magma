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
    pub symbol: String,
    /// Crate + module path, e.g. "mycrate::net::client".
    pub pkg: String,
    /// Repo-relative path.
    pub file: String,
    pub line: u32,
    /// 1-based UTF-8 byte column of the function's own name token, matching
    /// rustc's `column_start` convention in its JSON diagnostics (verified
    /// directly: a `trait \`DeadTr\` is never used` diagnostic for `pub trait
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
    /// "method" for anything with a `self` receiver.
    pub kind: String,
    /// `pub` visibility, syntactic: the item's own `pub` keyword, independent
    /// of whether an enclosing module is private. Root status (see `root`) is
    /// computed separately in `roots.rs` from *effective* visibility, because
    /// a `pub fn` in a private module is not part of the crate's public API.
    pub exported: bool,
    /// Declared in test code (`Function::is_test`).
    pub test: bool,
    /// An entry point: a bin `main`, or a lib `pub` item. Set in Task 5.
    pub root: bool,
    /// Benchmark function (`Function::is_bench`) — counts toward all-roots only.
    pub bench: bool,
    /// Build-script- or macro-generated code.
    pub generated: bool,
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
    /// Always `false`: a refusal means analysis never ran, so nothing
    /// executed. See `Output::executed_target_code`.
    pub executed_target_code: bool,
    pub computable: bool,
    pub reason: String,
}

impl Refusal {
    pub fn new(reason: impl Into<String>) -> Self {
        Refusal {
            contract_version: CONTRACT_VERSION.to_owned(),
            executed_target_code: false,
            computable: false,
            reason: reason.into(),
        }
    }
}
