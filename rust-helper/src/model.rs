//! The JSON contract between the helper and magma. The helper is an extractor:
//! it emits nodes and edges, never reachability or views — magma derives those.

use serde::Serialize;

/// Wire-format version of the helper's output. Bump on any breaking change.
pub const CONTRACT_VERSION: &str = "magma-rust-helper/1";

#[derive(Serialize)]
pub struct Output {
    pub contract_version: String,
    pub functions: Vec<Function>,
    pub calls: Vec<Call>,
}

impl Output {
    pub fn new(functions: Vec<Function>, calls: Vec<Call>) -> Self {
        Output { contract_version: CONTRACT_VERSION.to_owned(), functions, calls }
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
    /// "func" for free functions AND associated fns without a receiver;
    /// "method" for anything with a `self` receiver.
    pub kind: String,
    /// `pub` visibility. Load-bearing: for a library this also determines root
    /// status, because rustc treats a lib's public API as the root set.
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
