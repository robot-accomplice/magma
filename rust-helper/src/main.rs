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

    let mut cargo_config = CargoConfig::default();
    cargo_config.sysroot = Some(RustLibSource::Discover);
    cargo_config.cfg_overrides = CfgOverrides {
        global: CfgDiff::new(vec![CfgAtom::Flag(sym::test.clone())], Vec::new()),
        selective: Default::default(),
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
    // stays honest if a sandboxed or no-execution mode is ever added.
    let executed_target_code = load_config.load_out_dirs_from_check
        && load_config.with_proc_macro_server != ProcMacroServerChoice::None;

    // Contract defect 2 (not a cargo project / workspace load failure):
    // previously `?` propagated straight into anyhow, printing `Error: ...`
    // and exiting 1 with no JSON. `load_workspace_at` fails at
    // `ProjectManifest::discover_single` or `ProjectWorkspace::load` — both
    // BEFORE it runs build scripts (see `run_build_scripts`, called only
    // after both succeed) — so `executed_target_code` is honestly `false`
    // here: nothing of the target's own code ran.
    let (db, vfs, _p) =
        match load_workspace_at(Path::new(&root), &cargo_config, &load_config, &|_s| {}) {
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
    eprintln!("TIMING load_workspace: {:.1}s", t0.elapsed().as_secs_f64());

    // Same absolute-path computation load_workspace_at uses internally, so
    // stripping this prefix from vfs paths yields a repo-relative path.
    let root_abs = AbsPathBuf::assert_utf8(std::env::current_dir()?.join(&root));
    // H1 fix: the workspace's own target directory, used to anchor the
    // `generated` heuristic instead of an unanchored substring match (see
    // enumerate.rs). Resolved via `cargo metadata` rather than assuming
    // `<root>/target`, so a `CARGO_TARGET_DIR` override or a `target-dir`
    // setting in `.cargo/config.toml` is still honoured correctly.
    let target_dir_abs = discover_target_dir(&root_abs)?;

    let host = AnalysisHost::with_database(db);
    let analysis = host.analysis();
    let db = host.raw_database();

    let t1 = Instant::now();
    // Function ids are the index of the function in the enumeration vector.
    // Signature/type display requires the salsa db to be attached to this
    // thread (same requirement as the Semantics-based edge extraction below).
    let funcs: Vec<(model::Function, ra_ap_hir::Function)> = ra_ap_hir::attach_db(db, || {
        let sema = Semantics::new(db);
        let mut funcs =
            enumerate::collect(db, &sema, &vfs, root_abs.as_path(), target_dir_abs.as_path());
        roots::mark(db, &mut funcs);
        funcs
    });
    eprintln!(
        "TIMING enumerate: {:.1}s for {} functions",
        t1.elapsed().as_secs_f64(),
        funcs.len()
    );

    // Contract defect 2 (workspace with type/load errors): `load_workspace_at`
    // does NOT type-check on load — verified directly: a crate with a plain
    // type error (`let x: i32 = "not a number";`) loads and enumerates
    // cleanly with the code as it stood before this fix, producing a full,
    // silently-wrong Output rather than any refusal at all. magma's contract
    // must never hand back a partial or degraded map, so scan every
    // workspace-local real source file for a Severity::Error diagnostic
    // (RA's own semantic diagnostics — type mismatches, unresolved paths,
    // etc.) and refuse before ever reaching the roots/edges computation.
    // `executed_target_code` is honestly `true` here: `load_workspace_at`
    // already succeeded, running build scripts and expanding proc macros.
    let error_files =
        workspace_type_errors(&analysis, &vfs, root_abs.as_path(), target_dir_abs.as_path())?;
    if !error_files.is_empty() {
        let r = model::Refusal::new(
            executed_target_code,
            format!(
                "{} file(s) contain type/load errors; reachability not computable: {}",
                error_files.len(),
                error_files.join(", ")
            ),
        );
        println!("{}", serde_json::to_string_pretty(&r)?);
        std::process::exit(0);
    }

    if !funcs.iter().any(|(n, _)| n.root) {
        // Contract defect 1: `load_workspace_at` above already ran build
        // scripts and expanded proc macros by the time this fires, so
        // `executed_target_code` (computed from the actual load config, not
        // a hard-coded constant) is honestly `true` here — a refusal is not
        // automatically "nothing executed". Exit 0, not 2 (see EXIT_USAGE):
        // this is a real, computable answer, matching Go's convention.
        let r = model::Refusal::new(
            executed_target_code,
            "no roots in scope (no binary target and no public API); reachability not computable",
        );
        println!("{}", serde_json::to_string_pretty(&r)?);
        std::process::exit(0);
    }

    let index: HashMap<ra_ap_hir::Function, u32> =
        funcs.iter().map(|(mf, f)| (*f, mf.id)).collect();

    let t2 = Instant::now();
    let mut edges = 0usize;
    let mut calls: Vec<model::Call> = Vec::new();
    // Family C nodes (const/static/associated-const initializers) — see
    // enumerate::collect_inits. Populated only on the non-outgoing path,
    // same as `calls`; `--outgoing` is the pre-existing outgoing_calls
    // comparison mode and never emits Output/functions at all.
    let mut init_nodes: Vec<model::Function> = Vec::new();
    if use_outgoing {
        let cfg = CallHierarchyConfig { exclude_tests: false, ra_fixture: RaFixtureConfig::default() };
        for (mf, f) in &funcs {
            let Some(pos) = pos_of(db, *f) else { continue };
            if let Ok(Some(items)) = analysis.outgoing_calls(&cfg, pos) {
                for it in &items {
                    println!("EDGE\t{}\t{}", mf.symbol, it.target.name);
                }
                edges += items.len();
            }
        }
        eprintln!("MODE outgoing_calls");
    } else {
        // rust-analyzer's type inference requires the salsa DB attached to this
        // thread. Analysis::with_db does this internally; using Semantics directly
        // does not, and inference panics with "Try to use attached db, but not db
        // is attached".
        let init_call_count;
        (calls, init_nodes, init_call_count) = ra_ap_hir::attach_db(db, || {
            let sema = Semantics::new(db);
            let mut calls = walk::edges(&sema, db, &vfs, root_abs.as_path(), &funcs, &index);
            // Family C: gives every const/static/associated-const
            // initializer its own synthesized node (a real id in the
            // `functions` array, never an invented/dangling one), then
            // walks each initializer expression the same way a function
            // body is walked, so a call made from one is no longer
            // structurally invisible. See enumerate::collect_inits and
            // walk::init_edges. `funcs.len()` continues the id space real
            // functions already occupy.
            let inits = enumerate::collect_inits(
                db,
                &sema,
                &vfs,
                root_abs.as_path(),
                target_dir_abs.as_path(),
                funcs.len() as u32,
            );
            let init_edges = walk::init_edges(&sema, db, &vfs, root_abs.as_path(), &inits, &index);
            let init_call_count = init_edges.len();
            calls.extend(init_edges);
            let init_nodes: Vec<model::Function> = inits.into_iter().map(|(mf, _)| mf).collect();
            (calls, init_nodes, init_call_count)
        });
        edges = calls.len();
        eprintln!(
            "MODE semantics+macro-descent ({} initializer nodes, {} initializer edges)",
            init_nodes.len(),
            init_call_count
        );
    }
    eprintln!("TIMING edges: {:.1}s for {} edges", t2.elapsed().as_secs_f64(), edges);
    eprintln!("TIMING TOTAL: {:.1}s", t0.elapsed().as_secs_f64());

    if !use_outgoing {
        let mut functions: Vec<model::Function> = funcs.into_iter().map(|(mf, _f)| mf).collect();
        functions.extend(init_nodes);
        let out = model::Output::new(executed_target_code, functions, calls);
        println!("{}", serde_json::to_string_pretty(&out)?);
    }
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
