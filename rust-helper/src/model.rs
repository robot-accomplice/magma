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
}

/// One call edge. Endpoints are Function ids.
#[derive(Serialize)]
pub struct Call {
    pub from: u32,
    pub to: u32,
}
