// Spike: Semantics-based edge extraction with macro descent.
// Compares against outgoing_calls. usage: helper <root> [--outgoing]
mod enumerate;
mod model;
mod roots;
mod walk;

use std::collections::HashMap;
use std::path::Path;
use std::time::Instant;

use ra_ap_cfg::{CfgAtom, CfgDiff};
use ra_ap_hir::{HasSource, Semantics};
use ra_ap_ide::{
    Analysis, AnalysisHost, AssistResolveStrategy, CallHierarchyConfig, DiagnosticsConfig,
    FilePosition, Severity,
};
use ra_ap_ide_db::ra_fixture::RaFixtureConfig;
use ra_ap_ide_db::RootDatabase;
use ra_ap_intern::sym;
use ra_ap_load_cargo::{load_workspace_at, LoadCargoConfig, ProcMacroServerChoice};
use ra_ap_paths::{AbsPath, AbsPathBuf};
use ra_ap_project_model::{CargoConfig, CfgOverrides, RustLibSource};
use ra_ap_syntax::ast::HasName;
use ra_ap_syntax::AstNode;
use ra_ap_vfs::Vfs;

/// Exit codes. Deliberately mirrored on Go's convention rather than the
/// (inverted) convention this binary shipped with before Task C: a soft,
/// data-driven refusal (`computable:false` — no cargo project, no roots,
/// type errors) is a REAL ANSWER, not an error, so it exits 0 just like a
/// successful Output does; only usage misuse (a missing argument) — which
/// never even attempts analysis — gets a distinct nonzero code. Consistent
/// with Go's own split (refuse with 0, arg misuse with 2) so a future
/// magma-side Rust backend can drive both binaries identically.
const EXIT_USAGE: i32 = 2;

fn main() -> anyhow::Result<()> {
    let use_outgoing = std::env::args().any(|a| a == "--outgoing");
    let t0 = Instant::now();

    // Contract defect 2 (missing argument): previously `.expect(..)`, which
    // panics (exit 101) with no JSON at all — magma's contract requires
    // every soft-refusal path to emit a machine-readable `Refusal`, and a
    // panic is not that. Nothing has executed yet at this point, so
    // `executed_target_code` is honestly `false`.
    let Some(root) = std::env::args().nth(1) else {
        let r = model::Refusal::new(false, "usage: helper <workspace-root> [--outgoing]");
        println!("{}", serde_json::to_string_pretty(&r)?);
        std::process::exit(EXIT_USAGE);
    };

    let load_config = LoadCargoConfig {
        load_out_dirs_from_check: true,
        with_proc_macro_server: ProcMacroServerChoice::Sysroot,
        prefill_caches: false,
        num_worker_threads: 4,
        proc_macro_processes: 1,
    };
    // Whether loading the workspace ran the repo's own code: build scripts
    // executed and a proc-macro server expanded macros. Derived from the
    // config actually passed to load_workspace_at, not hard-coded, so it
    // stays honest if a sandboxed or no-execution mode is ever added. Shared
    // by both configs below — `load_config` itself never varies between
    // them, only `cfg_overrides` does.
    let executed_target_code = load_config.load_out_dirs_from_check
        && load_config.with_proc_macro_server != ProcMacroServerChoice::None;

    // Same absolute-path computation load_workspace_at uses internally, so
    // stripping this prefix from vfs paths yields a repo-relative path.
    let root_abs = AbsPathBuf::assert_utf8(std::env::current_dir()?.join(&root));
    // H1 fix: the workspace's own target directory, used to anchor the
    // `generated` heuristic instead of an unanchored substring match (see
    // enumerate.rs). Resolved via `cargo metadata` rather than assuming
    // `<root>/target`, so a `CARGO_TARGET_DIR` override or a `target-dir`
    // setting in `.cargo/config.toml` is still honoured correctly.
    let target_dir_abs = discover_target_dir(&root_abs)?;

    if use_outgoing {
        // Debug/comparison mode only (never emits Output; not part of the
        // JSON contract, not exercised by the oracle harness) — kept as the
        // single, permanently cfg(test)-on load it always was. Family E's
        // fix below is scoped to the real Output path.
        run_outgoing_mode(&root, &root_abs, &target_dir_abs, &load_config)?;
        return Ok(());
    }

    // Family E fix: TWO workspace loads, merged into one graph, instead of
    // one load with cfg(test) forced on globally and permanently. A single
    // cfg(test)-on load makes rust-analyzer's own item enumeration treat
    // every `#[cfg(not(test))]` item as if it did not exist — not merely
    // misclassified, structurally absent — so anything reachable only
    // through it read as false dead code. Go's backend takes the same two-
    // load shape for the identical reason (see internal/backend/golang.go).
    // `analyze_one_config`'s only difference between the two calls is
    // `cfg_overrides`; everything else (load_config, root, target_dir_abs)
    // is identical, so any behavioural difference between them comes
    // entirely from that one setting.
    let t_prod = Instant::now();
    let prod = analyze_one_config(
        &root,
        &root_abs,
        &target_dir_abs,
        &load_config,
        executed_target_code,
        CfgOverrides::default(), // cfg(test) OFF: matches an ordinary `cargo check`
    )?;
    eprintln!("TIMING prod-config (cfg(test) off): {:.1}s", t_prod.elapsed().as_secs_f64());
    let (prod, prod_has_root) = match prod {
        LoadOutcome::Refused(r) => {
            println!("{}", serde_json::to_string_pretty(&r)?);
            std::process::exit(0);
        }
        LoadOutcome::Ok { output, has_root } => (output, has_root),
    };

    let t_test = Instant::now();
    let test = analyze_one_config(
        &root,
        &root_abs,
        &target_dir_abs,
        &load_config,
        executed_target_code,
        CfgOverrides {
            global: CfgDiff::new(vec![CfgAtom::Flag(sym::test.clone())], Vec::new()),
            selective: Default::default(),
        }, // cfg(test) ON
    )?;
    eprintln!("TIMING test-config (cfg(test) on): {:.1}s", t_test.elapsed().as_secs_f64());
    let (test, test_has_root) = match test {
        LoadOutcome::Refused(r) => {
            println!("{}", serde_json::to_string_pretty(&r)?);
            std::process::exit(0);
        }
        LoadOutcome::Ok { output, has_root } => (output, has_root),
    };

    if !prod_has_root && !test_has_root {
        // Contract defect 1: both loads above already ran build scripts and
        // expanded proc macros by the time this fires, so
        // `executed_target_code` is honestly `true` here — a refusal is not
        // automatically "nothing executed". Exit 0, not 2 (see EXIT_USAGE):
        // this is a real, computable answer, matching Go's convention.
        // Checked only after BOTH configs are in, not either alone: a root
        // that exists only under one cfg (the exact Family E shape) must
        // still count — refusing here on one config's root set alone would
        // just move the false-dead-code defect one refusal earlier.
        let r = model::Refusal::new(
            executed_target_code,
            "no roots in scope (no binary target and no public API); reachability not computable",
        );
        println!("{}", serde_json::to_string_pretty(&r)?);
        std::process::exit(0);
    }

    let t_merge = Instant::now();
    let merged = merge_configs(prod, test);
    eprintln!(
        "TIMING merge: {:.1}s for {} functions, {} calls",
        t_merge.elapsed().as_secs_f64(),
        merged.functions.len(),
        merged.calls.len()
    );
    eprintln!("TIMING TOTAL: {:.1}s", t0.elapsed().as_secs_f64());

    // Family F: last line of defence before anything reaches a consumer.
    let escaped = non_local_paths(&merged.functions, &merged.calls);
    if !escaped.is_empty() {
        let r = model::Refusal::new(
            executed_target_code,
            format!(
                "{} node(s)/call site(s) resolve outside the workspace root; \
                 reachability not computable: {}",
                escaped.len(),
                escaped.join(", ")
            ),
        );
        println!("{}", serde_json::to_string_pretty(&r)?);
        std::process::exit(0);
    }

    let out = model::Output::new(executed_target_code, merged.functions, merged.calls);
    println!("{}", serde_json::to_string_pretty(&out)?);
    Ok(())
}

/// Every emitted `file`/`site_file` that names a location OUTSIDE the
/// workspace root — the Family F invariant, checked directly rather than via
/// a reachability fixture.
///
/// **Why this cannot be an oracle fixture.** The defect's original instance
/// (`#[derive(Debug)]` resolving into the sysroot) is invisible to the whole
/// harness by construction: rust-analyzer marks the derived method `root:true`
/// regardless of visibility, so the helper and rustc can never *disagree*
/// about its reachability and no `oracle-diff` comparison will ever go red on
/// it. `testdata/toolchain_derive` documents the shape; it is not coverage.
/// A direct assertion is the only check that can see this class at all.
///
/// **How the test works.** `enumerate::repo_relative_path` (which both node
/// metadata and `walk.rs`'s call-site metadata go through) strips the
/// workspace root and returns the untouched ABSOLUTE path when the strip
/// fails. So "the emitted string is absolute" is exactly, and only, "this
/// location escaped the workspace" — `is_absolute()` tests the fallback
/// branch itself, not a guess about path shape.
///
/// A hit is a refusal, not a filtered-out row, for the reason the rest of
/// this harness refuses: an unexplainable location means the map is degraded,
/// and magma's contract is an honest map or an honest refusal — never a quiet
/// partial one. Dropping the offending rows instead would be an exclusion,
/// and four exclusions in this harness have already been found unsound.
/// `enumerate::relocate_out_of_root` repairs the one shape that is understood
/// (builtin derives), so reaching here means a genuinely new shape that has
/// not been characterised — precisely when silence is most expensive.
fn non_local_paths(functions: &[model::Function], calls: &[model::Call]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for f in functions {
        if Path::new(&f.file).is_absolute() {
            out.push(format!("{} @ {}:{}", f.symbol, f.file, f.line));
        }
    }
    for c in calls {
        if Path::new(&c.site_file).is_absolute() {
            out.push(format!("call site {}:{}", c.site_file, c.site_line));
        }
    }
    out.sort();
    out.dedup();
    out
}

/// Nodes + edges from one full analysis pass, in that pass's OWN local id
/// space (0..N). Never compared or merged by id directly — two different
/// `load_workspace_at` calls produce two independent `RootDatabase`s, so a
/// `ra_ap_hir::Function`/id from one means nothing in the other. Only
/// `merge_configs` (keyed on file/line/column/symbol identity) may combine
/// two of these.
struct ConfigOutput {
    functions: Vec<model::Function>,
    calls: Vec<model::Call>,
}

/// Result of one `analyze_one_config` call: either a refusal (load failure or
/// type/load errors under that cfg setting), or a completed pass plus whether
/// IT ALONE found any root — `main` only refuses "no roots in scope" after
/// checking both configs together, so the caller needs this rather than a
/// bare `ConfigOutput`.
enum LoadOutcome {
    Refused(model::Refusal),
    Ok { output: ConfigOutput, has_root: bool },
}

/// One full analysis pass — workspace load, enumerate, roots, edges, Family C
/// init nodes/edges — under one `cfg_overrides` setting. Called twice by
/// `main` (cfg(test) off, then on) as the Family E fix. Identical to the
/// single-load body this replaced except for taking `cfg_overrides` as a
/// parameter instead of hard-coding cfg(test) on, and returning its result
/// instead of emitting `Output` directly (the two calls' results are merged
/// by `merge_configs` before anything is printed).
fn analyze_one_config(
    root: &str,
    root_abs: &AbsPathBuf,
    target_dir_abs: &AbsPathBuf,
    load_config: &LoadCargoConfig,
    executed_target_code: bool,
    cfg_overrides: CfgOverrides,
) -> anyhow::Result<LoadOutcome> {
    let mut cargo_config = CargoConfig::default();
    cargo_config.sysroot = Some(RustLibSource::Discover);
    cargo_config.cfg_overrides = cfg_overrides;

    // Contract defect 2 (not a cargo project / workspace load failure):
    // `load_workspace_at` fails at `ProjectManifest::discover_single` or
    // `ProjectWorkspace::load` — both BEFORE it runs build scripts (see
    // `run_build_scripts`, called only after both succeed) — so
    // `executed_target_code` is honestly `false` here: nothing of the
    // target's own code ran.
    let (db, vfs, _p) =
        match load_workspace_at(Path::new(root), &cargo_config, load_config, &|_s| {}) {
            Ok(v) => v,
            Err(e) => {
                return Ok(LoadOutcome::Refused(model::Refusal::new(
                    false,
                    format!("not a computable cargo workspace: {e:#}"),
                )));
            }
        };

    let host = AnalysisHost::with_database(db);
    let analysis = host.analysis();
    let db = host.raw_database();

    // Function ids are the index of the function in this config's OWN
    // enumeration vector — local to this pass, remapped by identity in
    // `merge_configs`. Signature/type display requires the salsa db to be
    // attached to this thread (same requirement as the Semantics-based edge
    // extraction below).
    // Family D adaptation: `mut` because `walk::edges` below now takes
    // `&mut` (it records a macro-expansion-depth-guard disclosure directly
    // on the originating node — see model::Function::macro_truncated).
    let mut funcs: Vec<(model::Function, ra_ap_hir::Function)> = ra_ap_hir::attach_db(db, || {
        let sema = Semantics::new(db);
        let mut funcs =
            enumerate::collect(db, &sema, &vfs, root_abs.as_path(), target_dir_abs.as_path());
        roots::mark(db, &mut funcs);
        funcs
    });

    // Contract defect 2 (workspace with type/load errors): `load_workspace_at`
    // does NOT type-check on load — verified directly: a crate with a plain
    // type error (`let x: i32 = "not a number";`) loads and enumerates
    // cleanly, producing a full, silently-wrong Output rather than any
    // refusal at all. magma's contract must never hand back a partial or
    // degraded map, so scan every workspace-local real source file for a
    // Severity::Error diagnostic (RA's own semantic diagnostics — type
    // mismatches, unresolved paths, etc.) and refuse before ever reaching
    // the edges computation. `executed_target_code` is honestly the passed-
    // in value here: `load_workspace_at` already succeeded, running build
    // scripts and expanding proc macros.
    let error_files =
        workspace_type_errors(&analysis, &vfs, root_abs.as_path(), target_dir_abs.as_path())?;
    if !error_files.is_empty() {
        return Ok(LoadOutcome::Refused(model::Refusal::new(
            executed_target_code,
            format!(
                "{} file(s) contain type/load errors; reachability not computable: {}",
                error_files.len(),
                error_files.join(", ")
            ),
        )));
    }

    let has_root = funcs.iter().any(|(n, _)| n.root);

    let index: HashMap<ra_ap_hir::Function, u32> =
        funcs.iter().map(|(mf, f)| (*f, mf.id)).collect();

    // rust-analyzer's type inference requires the salsa DB attached to this
    // thread. Analysis::with_db does this internally; using Semantics directly
    // does not, and inference panics with "Try to use attached db, but not db
    // is attached".
    let (calls, init_nodes) = ra_ap_hir::attach_db(db, || {
        let sema = Semantics::new(db);
        // `&mut funcs`: Family D adaptation, see the `mut` binding above.
        let mut calls = walk::edges(&sema, db, &vfs, root_abs.as_path(), &mut funcs, &index);
        // Family C: gives every const/static/associated-const initializer
        // its own synthesized node (a real id in this pass's `functions`
        // array, never an invented/dangling one), then walks each
        // initializer expression the same way a function body is walked, so
        // a call made from one is no longer structurally invisible. See
        // enumerate::collect_inits and walk::init_edges. `funcs.len()`
        // continues the id space real functions already occupy — still
        // purely local to this one pass.
        let mut inits = enumerate::collect_inits(
            db,
            &sema,
            &vfs,
            root_abs.as_path(),
            target_dir_abs.as_path(),
            funcs.len() as u32,
        );
        // `&mut inits`: Family D adaptation, same reason as `&mut funcs` above.
        let init_edges = walk::init_edges(&sema, db, &vfs, root_abs.as_path(), &mut inits, &index);
        calls.extend(init_edges);
        let init_nodes: Vec<model::Function> = inits.into_iter().map(|(mf, _)| mf).collect();
        (calls, init_nodes)
    });

    let mut functions: Vec<model::Function> = funcs.into_iter().map(|(mf, _f)| mf).collect();
    functions.extend(init_nodes);

    Ok(LoadOutcome::Ok { output: ConfigOutput { functions, calls }, has_root })
}

/// Node identity used to merge the two configs — deliberately NOT either
/// config's own `id` (meaningless across two independent `RootDatabase`s).
/// (file, line, column) is the declaration's own name-token position — see
/// `model::Function::column` — and does not move when only `cfg(test)`
/// changes; `symbol` is included because `enumerate::qualify_stem` already
/// makes it unique within a module for exactly this kind of identity
/// comparison.
///
/// **`pkg` is load-bearing, not belt-and-braces.** The original key omitted
/// it on the reasoning that two distinct methods never share (file, line,
/// column) — true for hand-written code, and false for a builtin `derive`,
/// whose declaration resolves to the *trait's* method in the sysroot (see
/// `enumerate::is_under_root`). That location is a per-trait CONSTANT, so two
/// workspace crates each deriving `Debug` for a type named `Widget` produced
/// byte-identical keys: one node was silently dropped by the merge below, and
/// `remap_and_aggregate` rewrote the survivor's id over both, fabricating an
/// edge from one crate's caller into the other crate's method. Reproduced on
/// a two-crate probe before the fix.
///
/// `enumerate`'s Family F relocation now moves those nodes onto per-type
/// local source, which dissolves the collision at its source; `pkg` stays in
/// the key regardless, so the merge cannot silently lose a node again if some
/// future shape reintroduces a shared location. Two members of one workspace
/// always differ in `pkg` (it is rooted at the crate's display name), and
/// `pkg` never varies with `cfg(test)`, so adding it cannot split a node that
/// should have merged.
type NodeKey = (String, String, u32, u32, String);

fn node_key(f: &model::Function) -> NodeKey {
    (f.pkg.clone(), f.file.clone(), f.line, f.column, f.symbol.clone())
}

/// Merges two full analysis passes — cfg(test) off (`prod`) and cfg(test) on
/// (`test`) — into one node/edge set. This is the Family E fix itself: under
/// the old single, permanently cfg(test)-on load, a `#[cfg(not(test))]` item
/// was structurally absent from enumeration, so it and anything reachable
/// only through it read as false dead code. Running both configs and taking
/// the union closes that hole by construction — a node the OLD single load
/// never saw now comes from `prod` instead.
///
/// **Merge design:**
/// - **Identity**: `node_key` (file, line, column, symbol) — see its doc
///   comment for why not an id from either pass.
/// - **A node in only one config is emitted as-is.** This IS the defect's
///   fix: under the old code such a node was silently absent, full stop.
/// - **A node in both configs becomes ONE node, ONE id.** Structural fields
///   (pkg, file, line, column, kind, exported, generated, signature, doc,
///   trait_impl, symbol) come from whichever side is encountered first
///   (`prod`, then `test`-only) — they describe the same source declaration
///   either way, so which side "wins" is not a correctness question. `root`
///   and `test` are OR'd across sides instead of taking either alone: both
///   are booleans where a false negative is the dangerous direction (a
///   dropped root hides a false-dead-code report; a dropped test flag
///   launders test code into a production root — see `roots::mark`'s own
///   doc comment), and OR is the only combinator that can never turn a true
///   in either source into a false in the output — "every imprecision must
///   fail toward live, never toward dead."
/// - **Edges**: each side's local `Call`s are remapped from local id to
///   global id via `node_key`, then re-aggregated per (from, to) with the
///   SAME static-upgrades-dynamic rule `walk::edges` already applies within
///   one pass — a pair seen as static in either config is real, so it must
///   end up static in the merged graph too, not silently lose that fact by
///   being aggregated separately per side.
///
/// Ids are assigned in prod-then-test-only order: every `prod` node keeps a
/// low, stable id block, and cfg(test)-only nodes are appended after — an
/// explainable, deterministic scheme, though the specific numbers are not a
/// contract magma depends on (`id` was always assignment-order-dependent).
fn merge_configs(prod: ConfigOutput, test: ConfigOutput) -> ConfigOutput {
    let prod_id_to_key: HashMap<u32, NodeKey> =
        prod.functions.iter().map(|f| (f.id, node_key(f))).collect();
    let test_id_to_key: HashMap<u32, NodeKey> =
        test.functions.iter().map(|f| (f.id, node_key(f))).collect();

    let mut merged: HashMap<NodeKey, model::Function> = HashMap::new();
    let mut order: Vec<NodeKey> = Vec::new();
    for f in prod.functions {
        let key = node_key(&f);
        order.push(key.clone());
        merged.insert(key, f);
    }
    for f in test.functions {
        let key = node_key(&f);
        match merged.get_mut(&key) {
            Some(existing) => {
                existing.root |= f.root;
                existing.test |= f.test;
            }
            None => {
                order.push(key.clone());
                merged.insert(key, f);
            }
        }
    }

    let mut key_to_global: HashMap<NodeKey, u32> = HashMap::new();
    let mut functions: Vec<model::Function> = Vec::with_capacity(order.len());
    for key in order {
        if key_to_global.contains_key(&key) {
            continue; // already assigned (both configs share a node — normal)
        }
        let global_id = functions.len() as u32;
        key_to_global.insert(key.clone(), global_id);
        let mut f = merged.remove(&key).expect("key was just inserted above");
        f.id = global_id;
        functions.push(f);
    }

    let mut agg: HashMap<(u32, u32), model::Call> = HashMap::new();
    remap_and_aggregate(prod.calls, &prod_id_to_key, &key_to_global, &mut agg);
    remap_and_aggregate(test.calls, &test_id_to_key, &key_to_global, &mut agg);

    let mut calls: Vec<model::Call> = agg.into_values().collect();
    calls.sort_by_key(|c| (c.from, c.to)); // deterministic output, matches walk::edges

    ConfigOutput { functions, calls }
}

/// Remaps one config's local-id `Call`s into the merged global id space via
/// `id_to_key`/`key_to_global`, aggregating into `agg` per (from, to) with
/// the same static-upgrades-dynamic rule `walk::edges` applies within a
/// single pass. An endpoint whose local id has no entry in `id_to_key` (or
/// whose key has no merged global id — should not happen, since every node
/// in `functions` was built from exactly these same per-config functions)
/// is skipped defensively rather than panicking.
fn remap_and_aggregate(
    calls: Vec<model::Call>,
    id_to_key: &HashMap<u32, NodeKey>,
    key_to_global: &HashMap<NodeKey, u32>,
    agg: &mut HashMap<(u32, u32), model::Call>,
) {
    for c in calls {
        let Some(from_key) = id_to_key.get(&c.from) else { continue };
        let Some(to_key) = id_to_key.get(&c.to) else { continue };
        let Some(&from) = key_to_global.get(from_key) else { continue };
        let Some(&to) = key_to_global.get(to_key) else { continue };
        agg.entry((from, to))
            .and_modify(|existing| {
                if c.kind == "static" {
                    existing.kind = "static".to_owned(); // static site upgrades the pair
                }
            })
            .or_insert(model::Call {
                from,
                to,
                site_file: c.site_file.clone(),
                site_line: c.site_line,
                kind: c.kind.clone(),
            });
    }
}

/// The pre-Family-E `--outgoing` debug/comparison mode: a single load, cfg(test)
/// forced on, printing raw `EDGE` lines via `Analysis::outgoing_calls` instead
/// of the JSON `Output` contract. Never emits `Output`/`functions`, not part
/// of the wire contract, not exercised by the oracle harness — kept
/// unchanged (still one config, not two) since Family E's fix is scoped to
/// the real Output path this function does not touch.
fn run_outgoing_mode(
    root: &str,
    root_abs: &AbsPathBuf,
    target_dir_abs: &AbsPathBuf,
    load_config: &LoadCargoConfig,
) -> anyhow::Result<()> {
    let mut cargo_config = CargoConfig::default();
    cargo_config.sysroot = Some(RustLibSource::Discover);
    cargo_config.cfg_overrides = CfgOverrides {
        global: CfgDiff::new(vec![CfgAtom::Flag(sym::test.clone())], Vec::new()),
        selective: Default::default(),
    };

    let (db, vfs, _p) =
        match load_workspace_at(Path::new(root), &cargo_config, load_config, &|_s| {}) {
            Ok(v) => v,
            Err(e) => {
                let r = model::Refusal::new(
                    false,
                    format!("not a computable cargo workspace: {e:#}"),
                );
                println!("{}", serde_json::to_string_pretty(&r)?);
                std::process::exit(0);
            }
        };

    let host = AnalysisHost::with_database(db);
    let analysis = host.analysis();
    let db = host.raw_database();

    let funcs: Vec<(model::Function, ra_ap_hir::Function)> = ra_ap_hir::attach_db(db, || {
        let sema = Semantics::new(db);
        let mut funcs =
            enumerate::collect(db, &sema, &vfs, root_abs.as_path(), target_dir_abs.as_path());
        roots::mark(db, &mut funcs);
        funcs
    });

    let cfg = CallHierarchyConfig { exclude_tests: false, ra_fixture: RaFixtureConfig::default() };
    let mut edges = 0usize;
    for (mf, f) in &funcs {
        let Some(pos) = pos_of(db, *f) else { continue };
        if let Ok(Some(items)) = analysis.outgoing_calls(&cfg, pos) {
            for it in &items {
                println!("EDGE\t{}\t{}", mf.symbol, it.target.name);
            }
            edges += items.len();
        }
    }
    eprintln!("MODE outgoing_calls ({edges} edges)");
    Ok(())
}

/// Resolves the workspace's actual target directory via `cargo metadata`
/// (the same technique `scripts/oracle-diff.sh` already trusts), rather than
/// assuming `<root>/target` — a `CARGO_TARGET_DIR` env var or a `target-dir`
/// key in `.cargo/config.toml` can relocate it. Used to anchor the
/// `generated` heuristic in `enumerate.rs` (Task H1): a function's file must
/// fall *under* this exact directory to count as generated, never merely
/// contain a `/target/` or `/build/` path segment.
fn discover_target_dir(root_abs: &AbsPathBuf) -> anyhow::Result<AbsPathBuf> {
    let output = std::process::Command::new("cargo")
        .args(["metadata", "--no-deps", "--format-version=1"])
        .current_dir(root_abs.as_path())
        .output()?;
    if !output.status.success() {
        anyhow::bail!(
            "cargo metadata failed while resolving the workspace's target directory: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    let meta: serde_json::Value = serde_json::from_slice(&output.stdout)?;
    let target_directory = meta
        .get("target_directory")
        .and_then(|v| v.as_str())
        .ok_or_else(|| anyhow::anyhow!("cargo metadata output missing target_directory"))?;
    Ok(AbsPathBuf::assert_utf8(std::path::PathBuf::from(target_directory)))
}

/// Repo-relative paths of every workspace-local `.rs` file (real files under
/// `root`, excluding `target_dir` — the same universe `enumerate::collect`
/// draws function nodes from, but at file granularity so a file with zero
/// functions, e.g. a struct-only file with a type error, is still covered)
/// carrying at least one `Severity::Error` diagnostic from rust-analyzer's
/// own semantic analysis. `load_workspace_at` never runs full type-checking
/// on load, so a workspace with genuine type/load errors otherwise loads and
/// enumerates as if nothing were wrong (confirmed directly: `let x: i32 =
/// "not a number";` produces a normal, silently-wrong Output with no
/// refusal). `Severity::Error` only — warnings and style lints are not
/// load/type errors and refusing on those would over-refuse a perfectly
/// computable workspace. `disable_experimental`/`style_lints: false` for the
/// same reason: only RA's core, non-experimental diagnostics gate this.
fn workspace_type_errors(
    analysis: &Analysis,
    vfs: &Vfs,
    root: &AbsPath,
    target_dir: &AbsPath,
) -> anyhow::Result<Vec<String>> {
    let config = DiagnosticsConfig {
        style_lints: false,
        disable_experimental: true,
        ..DiagnosticsConfig::test_sample()
    };
    let mut bad_files = Vec::new();
    for (file_id, vfs_path) in vfs.iter() {
        let Some(p) = vfs_path.as_path() else { continue };
        if !p.starts_with(root) || p.starts_with(target_dir) {
            continue;
        }
        if p.extension() != Some("rs") {
            continue;
        }
        let diags = analysis
            .full_diagnostics(&config, AssistResolveStrategy::None, file_id)
            .map_err(|_| anyhow::anyhow!("diagnostics computation was cancelled"))?;
        if diags.iter().any(|d| d.severity == Severity::Error) {
            bad_files.push(enumerate::repo_relative_path(vfs, root, file_id));
        }
    }
    bad_files.sort();
    Ok(bad_files)
}

fn pos_of(db: &RootDatabase, f: ra_ap_hir::Function) -> Option<FilePosition> {
    let src = f.source(db)?;
    let name = src.value.name()?;
    let offset = name.syntax().text_range().start();
    let file_id = src.file_id.file_id()?.file_id(db);
    Some(FilePosition { file_id, offset })
}
