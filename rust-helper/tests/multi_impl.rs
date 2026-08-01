//! Regression test for Task 6: `dyn`-dispatch edges must reach every impl.
//!
//! `testdata/multi_impl` dyn-dispatches `Speak::speak` on a `&dyn Speak`
//! bound to `Dog`, but also constructs a `Cat`. rustc's dead_code lint (the
//! oracle) reports zero "never used" warnings — it can't prove which impl
//! runs, and `Cat` is constructed, so it treats the whole chain, including
//! `Cat::speak` and `cat_target`, as live.
//!
//! Before the Task 6 fix, `resolve_method_call` on a `dyn Trait` receiver
//! resolved to the trait's own declared function, which is never enumerated
//! (absent from the id index), so the edge was dropped entirely — not just
//! for `Cat`, for EVERY impl reached only through `dyn`. `main -> Cat::speak
//! -> cat_target` was invisible, so a reachability view over this graph
//! would report `cat_target` dead. A graph that disagrees with the oracle
//! here reports false dead code, which is the one failure magma exists to
//! prevent.
//!
//! This test pins reachability by id, not by name. It used to say the two
//! impls "share the symbol `speak`", which was true when it was written and
//! is not any more: contract defect 5 qualified symbols, so they are now
//! `<Dog as Speak>::speak` and `<Cat as Speak>::speak`, and the trait's own
//! declaration is `Speak::speak`. The (symbol, line) lookup below is kept
//! anyway — it is now redundant for disambiguation but asserts the
//! qualification itself, which is the property that made it redundant.
//!
//! **This test was silently red for 26 commits.** Symbol qualification landed
//! in `f20e513`, 42 commits after this file was last touched, and nothing ran
//! `cargo test` — the CI workflow gated `gofmt`, `go vet` and the Go tests and
//! never built rust-helper at all. The Rust CI job added alongside this fix is
//! what stops that recurring; a test nothing runs is not a test.
//!
//! Must be run against the release binary (`cargo test --release`) per the
//! project rule: never validate this helper with a debug build.

use std::collections::{HashMap, HashSet};
use std::process::Command;

use serde_json::Value;

#[test]
fn cat_target_reachable_from_main_via_dyn_dispatch() {
    let bin = env!("CARGO_BIN_EXE_magma-rust-helper");
    let out = Command::new(bin)
        .arg("testdata/multi_impl")
        .output()
        .expect("failed to run helper binary");
    assert!(
        out.status.success(),
        "helper exited non-zero: {}",
        String::from_utf8_lossy(&out.stderr)
    );

    let graph: Value =
        serde_json::from_slice(&out.stdout).expect("helper stdout is not valid JSON");
    let functions = graph["functions"].as_array().expect("functions array");
    let calls = graph["calls"].as_array().expect("calls array");

    // (symbol, line) pins BOTH the id lookup and the qualified-symbol form.
    let id_at = |symbol: &str, line: u64| -> u32 {
        functions
            .iter()
            .find(|f| f["symbol"] == symbol && f["line"] == line)
            .unwrap_or_else(|| panic!("no function {symbol:?} at line {line}"))["id"]
            .as_u64()
            .unwrap() as u32
    };
    let main_id = id_at("main", 8);
    let cat_speak_id = id_at("<Cat as Speak>::speak", 5); // impl Speak for Cat
    let cat_target_id = id_at("cat_target", 7);

    let mut adj: HashMap<u32, Vec<u32>> = HashMap::new();
    for c in calls {
        let from = c["from"].as_u64().unwrap() as u32;
        let to = c["to"].as_u64().unwrap() as u32;
        adj.entry(from).or_default().push(to);
    }

    let mut seen = HashSet::new();
    let mut stack = vec![main_id];
    while let Some(n) = stack.pop() {
        if !seen.insert(n) {
            continue;
        }
        if let Some(succ) = adj.get(&n) {
            stack.extend(succ.iter().copied());
        }
    }

    assert!(
        seen.contains(&cat_speak_id),
        "Cat::speak not reachable from main via dyn dispatch — the dyn edge was dropped"
    );
    assert!(
        seen.contains(&cat_target_id),
        "cat_target not reachable from main — false dead code: rustc considers it live"
    );
}
