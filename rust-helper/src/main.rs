// Spike: Semantics-based edge extraction with macro descent.
// Compares against outgoing_calls. usage: helper <root> [--outgoing]
mod model;

use std::collections::HashMap;
use std::path::Path;
use std::time::Instant;

use ra_ap_cfg::{CfgAtom, CfgDiff};
use ra_ap_hir::{AssocItem, Crate, HasSource, ModuleDef, PathResolution, Semantics};
use ra_ap_ide::{AnalysisHost, CallHierarchyConfig, FilePosition};
use ra_ap_ide_db::base_db::CrateOrigin;
use ra_ap_ide_db::ra_fixture::RaFixtureConfig;
use ra_ap_ide_db::RootDatabase;
use ra_ap_intern::sym;
use ra_ap_load_cargo::{load_workspace_at, LoadCargoConfig, ProcMacroServerChoice};
use ra_ap_project_model::{CargoConfig, CfgOverrides};
use ra_ap_syntax::ast::{self, HasName};
use ra_ap_syntax::{AstNode, SyntaxNode};

fn main() -> anyhow::Result<()> {
    let root = std::env::args().nth(1).expect("usage: helper <workspace-root>");
    let use_outgoing = std::env::args().any(|a| a == "--outgoing");
    let t0 = Instant::now();

    let mut cargo_config = CargoConfig::default();
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
    let (db, _vfs, _p) =
        load_workspace_at(Path::new(&root), &cargo_config, &load_config, &|_s| {})?;
    eprintln!("TIMING load_workspace: {:.1}s", t0.elapsed().as_secs_f64());

    let host = AnalysisHost::with_database(db);
    let analysis = host.analysis();
    let db = host.raw_database();

    let t1 = Instant::now();
    let mut funcs: Vec<(String, FilePosition, ra_ap_hir::Function)> = Vec::new();
    for krate in Crate::all(db) {
        if !matches!(krate.origin(db), CrateOrigin::Local { .. }) {
            continue;
        }
        for module in krate.modules(db) {
            for decl in module.declarations(db) {
                if let ModuleDef::Function(f) = decl {
                    if let Some(p) = pos_of(db, f) {
                        funcs.push((f.name(db).as_str().to_owned(), p, f));
                    }
                }
            }
            for imp in module.impl_defs(db) {
                for item in imp.items(db) {
                    if let AssocItem::Function(f) = item {
                        if let Some(p) = pos_of(db, f) {
                            funcs.push((f.name(db).as_str().to_owned(), p, f));
                        }
                    }
                }
            }
        }
    }
    eprintln!(
        "TIMING enumerate: {:.1}s for {} functions",
        t1.elapsed().as_secs_f64(),
        funcs.len()
    );

    // Function ids are the index of the function in the enumeration vector.
    let functions: Vec<model::Function> = funcs
        .iter()
        .enumerate()
        .map(|(i, (n, _pos, _f))| model::Function { id: i as u32, symbol: n.clone() })
        .collect();
    let index: HashMap<ra_ap_hir::Function, u32> =
        funcs.iter().enumerate().map(|(i, (_n, _p, f))| (*f, i as u32)).collect();

    let t2 = Instant::now();
    let mut edges = 0usize;
    let mut calls: Vec<model::Call> = Vec::new();
    if use_outgoing {
        let cfg = CallHierarchyConfig { exclude_tests: false, ra_fixture: RaFixtureConfig::default() };
        for (n, pos, _f) in &funcs {
            if let Ok(Some(items)) = analysis.outgoing_calls(&cfg, *pos) {
                for it in &items {
                    println!("EDGE\t{}\t{}", n, it.target.name);
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
        edges = ra_ap_hir::attach_db(db, || {
            let sema = Semantics::new(db);
            let mut n_edges = 0usize;
            for (i, (_n, _pos, f)) in funcs.iter().enumerate() {
                let Some(src) = f.source(db) else { continue };
                if let Some(efid) = src.file_id.file_id() {
                    let _ = sema.parse(efid);
                }
                let Some(body) = src.value.body() else { continue };
                let mut targets = Vec::new();
                walk(&sema, body.syntax(), &mut targets, 0);
                for t in &targets {
                    // Calls into dependencies are out of scope (CrateOrigin::Local
                    // filter means `index` only has local functions).
                    if let Some(&to_id) = index.get(t) {
                        calls.push(model::Call { from: i as u32, to: to_id });
                    }
                }
                n_edges += targets.len();
            }
            n_edges
        });
        eprintln!("MODE semantics+macro-descent");
    }
    eprintln!("TIMING edges: {:.1}s for {} edges", t2.elapsed().as_secs_f64(), edges);
    eprintln!("TIMING TOTAL: {:.1}s", t0.elapsed().as_secs_f64());

    if !use_outgoing {
        let out = model::Output::new(functions, calls);
        println!("{}", serde_json::to_string_pretty(&out)?);
    }
    Ok(())
}

/// Walk a body, resolving calls. Descends into macro expansions — the thing
/// outgoing_calls fails to do, which caused false dead code for macro targets.
fn walk(
    sema: &Semantics<'_, RootDatabase>,
    node: &SyntaxNode,
    out: &mut Vec<ra_ap_hir::Function>,
    depth: usize,
) {
    if depth > 8 {
        return; // guard against pathological macro recursion
    }
    for n in node.descendants() {
        if let Some(mc) = ast::MacroCall::cast(n.clone()) {
            if let Some(exp) = sema.expand_macro_call(&mc) {
                walk(sema, &exp.value, out, depth + 1);
            }
        }
        if let Some(call) = ast::CallExpr::cast(n.clone()) {
            if let Some(ast::Expr::PathExpr(pe)) = call.expr() {
                if let Some(path) = pe.path() {
                    if let Some(PathResolution::Def(ModuleDef::Function(f))) =
                        sema.resolve_path(&path)
                    {
                        out.push(f);
                    }
                }
            }
        }
        if let Some(mcall) = ast::MethodCallExpr::cast(n.clone()) {
            if let Some(f) = sema.resolve_method_call(&mcall) {
                out.push(f);
            }
        }
    }
}

fn pos_of(db: &RootDatabase, f: ra_ap_hir::Function) -> Option<FilePosition> {
    let src = f.source(db)?;
    let name = src.value.name()?;
    let offset = name.syntax().text_range().start();
    let file_id = src.file_id.file_id()?.file_id(db);
    Some(FilePosition { file_id, offset })
}
