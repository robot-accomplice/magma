// magma's JavaScript/TypeScript analyser.
//
// stdout carries the magma-js-helper/1 envelope and NOTHING else; progress and
// timings go to stderr behind PROGRESS / TIMING prefixes. A refusal is a real
// answer — it exits 0 with an envelope. Only usage misuse exits 2, matching the
// Go and Rust backends so magma can drive all three identically.
'use strict';

const path = require('path');
const ts = require(path.join(__dirname, 'typescript.js'));

const EXIT_USAGE = 2;
const CONTRACT = 'magma-js-helper/1';

const progress = (s) => process.stderr.write(`PROGRESS ${s}\n`);

function emit(obj) {
  process.stdout.write(JSON.stringify({ contract_version: CONTRACT, ...obj }) + '\n');
}

function refuse(reason) {
  emit({ computable: false, reason, functions: null, calls: null });
  process.exit(0);
}

const repoArg = process.argv[2];
if (!repoArg) {
  emit({ computable: false, reason: 'usage: driver.js <repo>' });
  process.exit(EXIT_USAGE);
}
// Resolved once, and every later path derives from it. The Rust helper learned
// this the hard way: `/tmp` resolves to `/private/tmp` on macOS, so an
// unresolved root makes every prefix comparison silently miss.
const repo = require('fs').realpathSync(repoArg);

progress('discovering sources');
// Use TypeScript's own config discovery so magma analyses what the project
// does. allowJs/checkJs are forced on: the checker is a JavaScript analyser
// with an optional type layer, not a TS analyser tolerating JS, so plain .js
// is first-class rather than a degraded tier.
const configPath = ts.findConfigFile(repo, ts.sys.fileExists, 'tsconfig.json');
let fileNames = [];
let options = {};
if (configPath) {
  const parsed = ts.getParsedCommandLineOfConfigFile(configPath, {}, {
    ...ts.sys,
    onUnRecoverableConfigFileDiagnostic: () => {},
  });
  if (parsed) {
    fileNames = parsed.fileNames;
    options = parsed.options;
  }
}
if (fileNames.length === 0) {
  const exts = ['.ts', '.tsx', '.js', '.jsx', '.mts', '.cts', '.mjs', '.cjs'];
  fileNames = ts.sys.readDirectory(repo, exts, ['node_modules', 'dist', 'build', '.git']);
}
options = { ...options, allowJs: true, checkJs: true, noEmit: true };

if (fileNames.length === 0) {
  refuse('no JavaScript or TypeScript sources found in scope');
}

progress(`type-checking ${fileNames.length} files`);
const t0 = Date.now();
const program = ts.createProgram(fileNames, options);
const checker = program.getTypeChecker();
process.stderr.write(`TIMING program: ${((Date.now() - t0) / 1000).toFixed(1)}s\n`);

const rel = (f) => path.relative(repo, f);
const local = (sf) => !sf.isDeclarationFile && sf.fileName.startsWith(repo + path.sep);

const isFunctionLike = (n) =>
  ts.isFunctionDeclaration(n) || ts.isMethodDeclaration(n) ||
  ts.isFunctionExpression(n) || ts.isArrowFunction(n);

// A name for the declaration. An arrow assigned to a const has no name of its
// own, so the binding it is attached to is used — otherwise most modern JS
// would report as <anonymous>.
function declName(node) {
  if (node.name) return node.name.getText();
  const p = node.parent;
  if (p && (ts.isVariableDeclaration(p) || ts.isPropertyAssignment(p) ||
            ts.isPropertyDeclaration(p)) && p.name) {
    return p.name.getText();
  }
  return '<anonymous>';
}

// The symbol a call site can be resolved back to. For a named declaration that
// is its own symbol; for an arrow bound to a name it is the binding's.
function declSymbol(node) {
  if (node.name) return checker.getSymbolAtLocation(node.name);
  const p = node.parent;
  if (p && p.name) return checker.getSymbolAtLocation(p.name);
  return undefined;
}

progress('collecting declarations');
const functions = [];
const idBySymbol = new Map();
const idByNode = new Map();

for (const sf of program.getSourceFiles()) {
  if (!local(sf)) continue;
  const isTest = /\.(test|spec)\.[cm]?[jt]sx?$/.test(sf.fileName) ||
                 /(^|\/)__tests__\//.test(rel(sf.fileName));
  const visit = (node) => {
    if (isFunctionLike(node)) {
      const { line } = sf.getLineAndCharacterOfPosition(node.getStart());
      const id = functions.length;
      functions.push({
        id,
        symbol: declName(node),
        pkg: path.dirname(rel(sf.fileName)),
        file: rel(sf.fileName),
        line: line + 1,
        kind: ts.isMethodDeclaration(node) ? 'method' : 'func',
        exported: (ts.getCombinedModifierFlags(node) & ts.ModifierFlags.Export) !== 0,
        test: isTest,
        // Roots are NOT set here. Framework entry points, package exports and
        // script targets land in Plan 2; until then this is honestly false and
        // the backend declares js-roots-not-yet-framework-aware as a
        // limitation rather than letting the gap pass unstated.
        root: false,
        generated: false,
      });
      idByNode.set(node, id);
      const sym = declSymbol(node);
      if (sym) idBySymbol.set(sym, id);
    }
    ts.forEachChild(node, visit);
  };
  ts.forEachChild(sf, visit);
}

progress('resolving calls');
const calls = [];
let unresolved = 0;

// The function a call site sits inside. Walks to the nearest enclosing
// function-like node rather than matching on names, so calls written inside an
// anonymous callback are still attributed to it.
function enclosingId(node) {
  for (let p = node.parent; p; p = p.parent) {
    if (idByNode.has(p)) return idByNode.get(p);
  }
  return -1;
}

for (const sf of program.getSourceFiles()) {
  if (!local(sf)) continue;
  const visit = (node) => {
    if (ts.isCallExpression(node) || ts.isNewExpression(node)) {
      const from = enclosingId(node);
      const sig = checker.getResolvedSignature(node);
      const decl = sig && sig.declaration;
      let to;
      if (decl && idByNode.has(decl)) {
        to = idByNode.get(decl);
      } else if (decl) {
        const sym = declSymbol(decl);
        if (sym && idBySymbol.has(sym)) to = idBySymbol.get(sym);
      }
      if (to !== undefined) {
        const { line } = sf.getLineAndCharacterOfPosition(node.getStart());
        calls.push({
          from,
          to,
          site_file: rel(sf.fileName),
          site_line: line + 1,
          kind: 'static',
        });
      } else {
        // LOUD, not silent. A computed member call, a dynamic import() or a
        // require() with a variable specifier has no static target — that is a
        // fact about JavaScript. An UNCOUNTED one is a defect, because it makes
        // a limit indistinguishable from an absence.
        unresolved++;
      }
    }
    ts.forEachChild(node, visit);
  };
  ts.forEachChild(sf, visit);
}

process.stderr.write(`TIMING total: ${((Date.now() - t0) / 1000).toFixed(1)}s\n`);
emit({
  computable: true,
  // Type-check only: no build scripts run, no plugins load, no target code
  // executes. Unlike the Rust helper, which must run build.rs to load a
  // workspace at all.
  executed_target_code: false,
  unresolved_call_sites: unresolved,
  functions,
  calls,
});
