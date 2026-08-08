// magma's JavaScript/TypeScript analyser.
//
// stdout carries the magma-js-helper/1 envelope and NOTHING else; progress and
// timings go to stderr behind PROGRESS / TIMING prefixes. A refusal is a real
// answer — it exits 0 with an envelope. Only usage misuse exits 2, matching the
// Go and Rust backends so magma can drive all three identically.
'use strict';

const path = require('node:path');
const fs = require('node:fs');
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
const repo = fs.realpathSync(repoArg);

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

// Build output is not source. A tsconfig commonly `include`s generated
// directories (Next.js emits .next/types/*.ts and references them), so
// filtering only at directory-scan time misses them — measured: `.next/types/
// validator.ts` was collected and then reported as dead code, which is a
// deletion order for a file the build regenerates.
const BUILD_OUTPUT = /(^|\/)(\.next|\.svelte-kit|\.turbo|\.nuxt|dist|build|out|coverage)\//;
const local = (sf) =>
  !sf.isDeclarationFile &&
  sf.fileName.startsWith(repo + path.sep) &&
  !BUILD_OUTPUT.test(rel(sf.fileName));

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

// The graph id for a declaration, if the graph knows one. A symbol's
// declaration for `const Page = () => {}` is the VARIABLE, whose initializer is
// the function node actually collected — so both have to be tried.
function idOfDeclaration(d) {
  if (!d) return undefined;
  if (idByNode.has(d)) return idByNode.get(d);
  return d.initializer ? idByNode.get(d.initializer) : undefined;
}

// The symbol a call site can be resolved back to. For a named declaration that
// is its own symbol; for an arrow bound to a name it is the binding's.
function declSymbol(node) {
  if (node.name) return checker.getSymbolAtLocation(node.name);
  const p = node.parent;
  if (p && p.name) return checker.getSymbolAtLocation(p.name);
  return undefined;
}

// ---------------------------------------------------------------------------
// Roots.
//
// ROOTS ARE DATA. A new framework is a row in a table plus a fixture, never a
// new branch inside a function. This is the walk() lesson applied before the
// debt accrues rather than after: the Rust helper's walk() reached 316 lines
// because every new form was bolted on as an edit, and one root rule per
// framework is the same growth pressure, forever.
//
// AN ORDINARY MODULE'S EXPORT IS NOT A ROOT. This is the load-bearing call. In
// JavaScript nearly everything is exported, so rooting every export reproduces
// the measured Bevy failure exactly: 87% of nodes rooted, ZERO dead functions
// reported out of 156, and no discriminating power left. An internal export
// earns liveness by being IMPORTED, not by being exported.
// ---------------------------------------------------------------------------

// Framework and test entry points, matched on the repo-relative path. Entry
// points nothing in the repo imports: the framework or the test runner calls
// them, so they have no incoming edge and would otherwise all report dead.
//
// TEST RULES COME FIRST, and the order is load-bearing: the first rule to claim
// a file wins. A file mis-claimed by a framework rule would become a PRODUCTION
// root, inflating production reachability and hiding real dead code, so where
// two rules could overlap the conservative claim must win.
const PATH_ROOT_RULES = [
  { id: 'test-file', test: true, re: /\.(test|spec)\.[cm]?[jt]sx?$/ },
  { id: 'tests-dir', test: true, re: /(^|\/)__tests__\// },
  // Runner setup files, by convention rather than by reading a config. Named in
  // vitest/jest config as a plain string, so nothing imports them and they
  // otherwise report dead. Reading the config itself stays a non-goal: it would
  // mean executing the target's JavaScript, which magma never does.
  { id: 'test-setup', test: true, re: /(^|\/)(test\/setup|vitest\.setup|jest\.setup|setupTests)\.[cm]?[jt]sx?$/ },
  { id: 'next-app-router', test: false, re: /(^|\/)app\/(.+\/)?(page|layout|route|template|default|loading|error|global-error|not-found)\.[cm]?[jt]sx?$/ },
  { id: 'next-pages-router', test: false, re: /(^|\/)pages\/.+\.[cm]?[jt]sx?$/ },
  { id: 'next-middleware', test: false, re: /(^|\/)middleware\.[cm]?[jt]s$/ },
  { id: 'next-instrumentation', test: false, re: /(^|\/)instrumentation\.[cm]?[jt]s$/ },
  { id: 'sveltekit-route', test: false, re: /(^|\/)\+(page|layout|server|error)(\.[a-z]+)?\.[cm]?[jt]s$/ },
  // Tooling configs are entry points loaded by the tool, never imported by the
  // app. Measured on a real Next.js repo: without these rows, next.config.ts,
  // vitest.config.ts and the three sentry configs were all listed for deletion.
  // Adding a framework is a ROW — this is the table paying for itself.
  { id: 'tooling-config', test: false, re: /(^|\/)(next|vite|vitest|jest|playwright|tailwind|postcss|eslint|svelte|rollup|webpack|drizzle)\.config\.[cm]?[jt]s$/ },
  { id: 'sentry-config', test: false, re: /(^|\/)sentry\.(client|server|edge)\.config\.[cm]?[jt]s$/ },
  { id: 'next-instrumentation-client', test: false, re: /(^|\/)instrumentation-client\.[cm]?[jt]s$/ },
];

const TEST_RULE_IDS = new Set(PATH_ROOT_RULES.filter((r) => r.test).map((r) => r.id));

// package.json fields that declare a callable surface. A published package's
// exports are its callable-from-outside API — the same reasoning that makes
// Rust's `pub` API a root — and a script target names a real entry
// (`node server.js`). Each row says only how to pull candidate paths out of
// its own field's shape.
const PACKAGE_ROOT_RULES = [
  { id: 'pkg-main', pick: (p) => [p.main] },
  { id: 'pkg-module', pick: (p) => [p.module] },
  { id: 'pkg-bin', pick: (p) => (typeof p.bin === 'string' ? [p.bin] : Object.values(p.bin || {})) },
  { id: 'pkg-exports', pick: (p) => stringLeaves(p.exports) },
  { id: 'pkg-scripts', pick: (p) => Object.values(p.scripts || {}).flatMap(scriptPaths) },
];

// `exports` is an arbitrarily nested map of conditions ("import", "require",
// "node", "./sub") whose leaves are paths. Only the leaves matter here.
function stringLeaves(v) {
  if (typeof v === 'string') return [v];
  if (v && typeof v === 'object') return Object.values(v).flatMap(stringLeaves);
  return [];
}

// A script is a shell command, not a path. Take the tokens that look like
// source files and let resolution reject the rest — deliberately NOT executing
// or shell-parsing it, since magma never runs target code.
function scriptPaths(cmd) {
  return String(cmd).split(/\s+/).filter((t) => /\.[cm]?[jt]sx?$/.test(t));
}

// Read through ts.sys, as every other filesystem access in this driver does
// (findConfigFile, readDirectory, fileExists). The raw fs read was the odd one
// out, and reading the target's tree through the compiler's own host is both
// consistent and the layer that already owns path handling here.
let pkg = {};
try {
  pkg = JSON.parse(ts.sys.readFile(path.join(repo, 'package.json')) || '{}');
} catch {
  // No package.json, or unparseable. Not a refusal: the path rules above still
  // apply, and a repo where NO rule matches refuses later, with a reason.
  pkg = {};
}

// Every local source file, repo-relative, for resolving the declared surface
// onto real sources.
const localFiles = new Set();
for (const sf of program.getSourceFiles()) {
  if (local(sf)) localFiles.add(rel(sf.fileName));
}

// A declared path is a module specifier, not necessarily a file: it may omit
// the extension or name a directory with an index. Anything unresolvable — a
// `dist/` build output, a shell flag picked out of a script — simply matches
// nothing. That silence is correct: rooting nothing costs a little liveness,
// while over-rooting destroys the analysis outright.
const CANDIDATE_EXTS = ['', '.ts', '.tsx', '.js', '.jsx', '.mts', '.cts', '.mjs', '.cjs'];
function resolveDeclared(p) {
  if (typeof p !== 'string' || p === '') return undefined;
  const base = path.normalize(p).replace(/^\.\//, '');
  for (const ext of CANDIDATE_EXTS) {
    if (localFiles.has(base + ext)) return base + ext;
  }
  for (const ext of CANDIDATE_EXTS.slice(1)) {
    const idx = path.posix.join(base, 'index' + ext);
    if (localFiles.has(idx)) return idx;
  }
  return undefined;
}

// file -> the id of the rule that rooted it. First claim wins, so the table
// order above decides overlaps.
const ruleByFile = new Map();
const claim = (file, id) => {
  if (file && localFiles.has(file) && !ruleByFile.has(file)) ruleByFile.set(file, id);
};
for (const rule of PATH_ROOT_RULES) {
  for (const f of localFiles) {
    if (rule.re.test(f)) claim(f, rule.id);
  }
}
for (const rule of PACKAGE_ROOT_RULES) {
  for (const p of rule.pick(pkg) || []) claim(resolveDeclared(p), rule.id);
}

// Counted per rule so over-rooting surfaces as a NUMBER during development
// rather than as a discovery on a real repository months later. Carried on the
// helper envelope but deliberately NOT into the artifact's `disclosure`:
// architext's disclosure object sets additionalProperties:false over exactly
// five keys, so a sixth would reject the whole document. Jon's call 2026-08-08
// — build roots now, hold the emit until they ack the field.
const rootsByRule = {};
const countRoot = (id) => { rootsByRule[id] = (rootsByRule[id] || 0) + 1; };

progress('collecting declarations');
const functions = [];
const idBySymbol = new Map();
const idByNode = new Map();
const moduleIdByFile = new Map();

for (const sf of program.getSourceFiles()) {
  if (!local(sf)) continue;
  const relPath = rel(sf.fileName);
  const rule = ruleByFile.get(relPath);
  // A root FILE. Its module scope is an entry point, and so is anything it
  // exports — the framework or the runner calls those, and nothing in the repo
  // does. In any OTHER file an export is emphatically not a root.
  const isRootFile = rule !== undefined;
  const isTest = TEST_RULE_IDS.has(rule);

  // A node for the module's own top level, BEFORE its declarations so it owns
  // the lowest id in the file.
  //
  // Module scope is executable code in JavaScript — `main()` at the foot of an
  // index.js is a real call from a real place — but it is not inside any
  // function, so it had no node to be attributed to. The previous sentinel
  // (-1) was not merely inelegant: it left the graph, and `ids[-1]` on a
  // map[int]string in the architext emit is a zero-value read, so it arrived
  // downstream as `"from": ""`. That fails architext's id pattern and rejects
  // the whole artifact, and where the two endpoints' module slugs differed it
  // dereferenced a nil module and PANICKED the emit outright.
  //
  // `init` is the kind the Rust backend already uses for synthesized
  // initializers, and it is already in the consumer's enum, so this needs no
  // contract change.
  const moduleID = functions.length;
  functions.push({
    id: moduleID,
    symbol: '<module>',
    pkg: path.dirname(rel(sf.fileName)),
    file: rel(sf.fileName),
    line: 1,
    kind: 'init',
    // Module scope is not an export, and rooting is decided by the root rules
    // — never by a node merely existing.
    exported: false,
    test: isTest,
    root: isRootFile,
    generated: false,
  });
  moduleIdByFile.set(sf.fileName, moduleID);
  if (isRootFile) countRoot(rule);

  const visit = (node) => {
    if (isFunctionLike(node)) {
      const { line } = sf.getLineAndCharacterOfPosition(node.getStart());
      const id = functions.length;
      const exported = (ts.getCombinedModifierFlags(node) & ts.ModifierFlags.Export) !== 0;
      functions.push({
        id,
        symbol: declName(node),
        pkg: path.dirname(rel(sf.fileName)),
        file: rel(sf.fileName),
        line: line + 1,
        kind: ts.isMethodDeclaration(node) ? 'method' : 'func',
        exported,
        test: isTest,
        // Rooted in a second pass, through the checker — see below.
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

// A root file's exports are entry points, resolved through the CHECKER rather
// than by reading modifier flags.
//
// `export default Page` where Page is a const-bound arrow carries no Export
// modifier on the arrow itself, and that is one of the two most common ways a
// Next.js page is written. A flags-only reading would leave it unrooted and
// report the page DEAD — a false dead is a deletion order, and this is exactly
// the semantic question the vendored compiler is here to answer.
//
// Confined to root FILES. Doing this everywhere is the measured Bevy failure:
// in JavaScript nearly everything is exported, so rooting every export leaves
// no discriminating power at all.
for (const sf of program.getSourceFiles()) {
  if (!local(sf)) continue;
  const rule = ruleByFile.get(rel(sf.fileName));
  if (rule === undefined) continue;
  const moduleSym = checker.getSymbolAtLocation(sf);
  if (!moduleSym) continue;
  for (const ex of checker.getExportsOfModule(moduleSym)) {
    const target = (ex.flags & ts.SymbolFlags.Alias) ? checker.getAliasedSymbol(ex) : ex;
    for (const d of target.declarations || []) {
      const id = idOfDeclaration(d);
      if (id !== undefined && !functions[id].root) {
        functions[id].root = true;
        countRoot(rule);
      }
    }
  }
}

progress('resolving calls');
const calls = [];
let unresolved = 0;

// The function a call site sits inside. Walks to the nearest enclosing
// function-like node rather than matching on names, so calls written inside an
// anonymous callback are still attributed to it.
//
// Falls back to the file's module node, never to a sentinel: a call at module
// scope has a real origin, and EVERY EMITTED EDGE MUST NAME A DECLARED NODE.
// That invariant is the consumer's, not a stylistic preference — architext
// rejects an artifact whose call references an id it cannot resolve.
function enclosingId(node, sf) {
  for (let p = node.parent; p; p = p.parent) {
    if (idByNode.has(p)) return idByNode.get(p);
  }
  return moduleIdByFile.get(sf.fileName);
}

// A function handed to something else as a VALUE gets invoked by whatever
// received it — `arr.map(cb)`, `useEffect(cb)`, `onClick={cb}`, `it('x', cb)`.
// Nothing in the graph calls it syntactically, so without this every callback
// has no incoming edge and reports dead. Measured on a real Next.js repo:
// 659 of 750 nodes dead, overwhelmingly anonymous callbacks. That is the
// mirror image of over-rooting and the more dangerous half — a dead row is a
// deletion order, so mass false-dead is worse than mass false-live.
//
// This is the Rust family-A defect in JavaScript form: a value passed where a
// call was expected has no syntactic call site.
//
// The edge runs from the ENCLOSING scope rather than from the callee, because
// magma cannot see inside `map` or `useEffect` to know when they invoke it.
// Deliberately NARROW — argument position and JSX attribute only. The tempting
// general rule ("any function expression is live from its enclosing scope")
// would make an unused `const helper = () => {}` permanently live, and
// arrow-bound helpers are exactly the dead code worth finding.
// The functions an expression in a value position hands over. Resolves by
// SHAPE, because all four shapes are ordinary in a React/Node codebase and
// each one produced false dead code on a real repository:
//
//   f(() => {})                 an inline function
//   addEventListener(onClick)   a named function passed by reference
//   useSWR(url, { onSuccess })  a function inside an options/mock object
//   Promise.all([a, b])         a function inside an array
//
// An identifier that does not resolve to a function we know contributes
// nothing, so this widens edges only where a real handoff exists.

// The graph id a symbol resolves to, following import aliases.
function idOfSymbol(sym) {
  if (sym && (sym.flags & ts.SymbolFlags.Alias)) sym = checker.getAliasedSymbol(sym);
  return sym ? idBySymbol.get(sym) : undefined;
}

// The operators that pass one of their operands through unchanged.
const PASSTHROUGH_OPERATORS = new Set([
  ts.SyntaxKind.AmpersandAmpersandToken,
  ts.SyntaxKind.BarBarToken,
  ts.SyntaxKind.QuestionQuestionToken,
]);

// Wrappers that choose or merely re-type the value being handed over. All of
// them still hand it over, so stopping at the wrapper loses the edge:
// `onClick={isConnected ? handleSwap : onConnect}` reported handleSwap DEAD on
// a real repo — the primary action of a swap screen, marked for deletion.
function passthroughOperands(expr) {
  if (ts.isConditionalExpression(expr)) return [expr.whenTrue, expr.whenFalse];
  if (ts.isBinaryExpression(expr) && PASSTHROUGH_OPERATORS.has(expr.operatorToken.kind)) {
    return [expr.left, expr.right];
  }
  if (ts.isParenthesizedExpression(expr) || ts.isAsExpression(expr) ||
      ts.isNonNullExpression(expr) || ts.isSatisfiesExpression(expr)) {
    return [expr.expression];
  }
  return [];
}

// The functions an object literal hands over, one property at a time.
function objectLiteralHandoffs(expr, out, depth) {
  for (const p of expr.properties) {
    if (ts.isPropertyAssignment(p)) {
      handedOff(p.initializer, out, depth + 1);
    } else if (ts.isMethodDeclaration(p) && idByNode.has(p)) {
      out.push(idByNode.get(p));
    } else if (ts.isShorthandPropertyAssignment(p)) {
      // `{ onDone }` needs its own accessor: getSymbolAtLocation on the
      // identifier yields the PROPERTY symbol, not the value it stands for,
      // so resolving it like a normal identifier silently finds nothing.
      const id = idOfSymbol(checker.getShorthandAssignmentValueSymbol(p));
      if (id !== undefined) out.push(id);
    }
  }
}

function handedOff(expr, out, depth) {
  if (!expr || depth > 3) return;
  if (isFunctionLike(expr)) {
    if (idByNode.has(expr)) out.push(idByNode.get(expr));
    return;
  }
  if (ts.isIdentifier(expr) || ts.isPropertyAccessExpression(expr)) {
    const id = idOfSymbol(checker.getSymbolAtLocation(expr));
    if (id !== undefined) out.push(id);
    return;
  }
  if (ts.isObjectLiteralExpression(expr)) {
    objectLiteralHandoffs(expr, out, depth);
    return;
  }
  if (ts.isArrayLiteralExpression(expr)) {
    for (const el of expr.elements) handedOff(el, out, depth + 1);
    return;
  }
  for (const operand of passthroughOperands(expr)) handedOff(operand, out, depth + 1);
}

// The value positions themselves: a call's arguments, a JSX attribute, and a
// return value.
//
// A RETURNED closure is a handoff too — whoever receives it calls it. This is
// how every `useEffect` cleanup is written (`return () => { cancelled = true }`),
// and without it each one reports dead on any React codebase.
function valuePositions(node) {
  if (ts.isCallExpression(node) || ts.isNewExpression(node)) return node.arguments || [];
  if (ts.isJsxAttribute(node) && node.initializer && ts.isJsxExpression(node.initializer)) {
    return node.initializer.expression ? [node.initializer.expression] : [];
  }
  if (ts.isReturnStatement(node) && node.expression) return [node.expression];
  return [];
}

// The declaration a call site targets. A JSX tag resolves through the checker
// so a renamed import lands on the right declaration; a lowercase intrinsic
// (`<div>`) resolves to nothing local and is simply not an edge.
function calleeDeclaration(node, isJsx) {
  if (!isJsx) {
    const sig = checker.getResolvedSignature(node);
    return sig?.declaration;
  }
  let sym = checker.getSymbolAtLocation(node.tagName);
  if (sym && (sym.flags & ts.SymbolFlags.Alias)) sym = checker.getAliasedSymbol(sym);
  const d = sym?.declarations?.[0];
  if (!d) return undefined;
  return idByNode.has(d) ? d : (d.initializer || d);
}

// Record every function this node hands to something else.
function recordHandoffs(node, sf) {
  const positions = valuePositions(node);
  if (positions.length === 0) return;
  const targets = [];
  for (const p of positions) handedOff(p, targets, 0);
  if (targets.length === 0) return;
  const owner = enclosingId(node, sf);
  if (owner === undefined) return;
  const { line } = sf.getLineAndCharacterOfPosition(node.getStart());
  for (const to of targets) {
    if (to === owner) continue;
    calls.push({
      from: owner,
      to,
      site_file: rel(sf.fileName),
      site_line: line + 1,
      kind: 'static',
    });
  }
}

for (const sf of program.getSourceFiles()) {
  if (!local(sf)) continue;
  const visit = (node) => {
    recordHandoffs(node, sf);
    // A JSX element IS a call: `<Foo />` invokes Foo. Without this every React
    // component has no incoming edge and reports dead — the direct analogue of
    // the Rust family-A defect, where Bevy systems passed as values had no
    // syntactic call site.
    const isJsx = ts.isJsxSelfClosingElement(node) || ts.isJsxOpeningElement(node);
    if (ts.isCallExpression(node) || ts.isNewExpression(node) || isJsx) {
      const from = enclosingId(node, sf);
      const decl = calleeDeclaration(node, isJsx);
      let to;
      if (decl && idByNode.has(decl)) {
        to = idByNode.get(decl);
      } else if (decl) {
        to = idOfSymbol(declSymbol(decl));
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
      } else if (!isJsx || /^[A-Z]/.test(node.tagName.getText())) {
        // LOUD, not silent. A computed member call, a dynamic import() or a
        // require() with a variable specifier has no static target — that is a
        // fact about JavaScript. An UNCOUNTED one is a defect, because it makes
        // a limit indistinguishable from an absence.
        //
        // An intrinsic element (`<div>`, lowercase by React's convention) is
        // excluded: it is not a call into any user function, so counting it
        // would inflate the number on every React repository and make the
        // figure meaningless where it matters most. A capitalised tag IS a
        // component reference, so an unresolvable one still counts.
        unresolved++;
      }
    }
    ts.forEachChild(node, visit);
  };
  ts.forEachChild(sf, visit);
}

progress('resolving imports');
// An import IS an invocation of the imported module's top level.
//
// This does not breach "magma models calls, not uses". Importing a module
// EXECUTES its module scope — that is what an import does in JavaScript — so
// the edge records a real invocation of real code, not a mere reference. The
// edge is module-to-module and deliberately goes no further: it makes the
// imported module live without making its exports live, so an imported module's
// unused export still reports dead and the analysis keeps its teeth.
//
// Without this, EVERY non-root module reports dead — measured on a three-file
// fixture, where an imported `lib.js` was listed for deletion. A false dead is
// a deletion order, so this is a correctness requirement, not a refinement.
for (const sf of program.getSourceFiles()) {
  if (!local(sf)) continue;
  const from = moduleIdByFile.get(sf.fileName);
  const visitImports = (node) => {
    const spec = (ts.isImportDeclaration(node) || ts.isExportDeclaration(node))
      ? node.moduleSpecifier
      : undefined;
    if (spec) {
      const sym = checker.getSymbolAtLocation(spec);
      const target = sym && (sym.declarations || []).find(ts.isSourceFile);
      const to = target && moduleIdByFile.get(target.fileName);
      if (to !== undefined && to !== from) {
        const { line } = sf.getLineAndCharacterOfPosition(node.getStart());
        calls.push({
          from,
          to,
          site_file: rel(sf.fileName),
          site_line: line + 1,
          kind: 'static',
        });
      }
    }
    ts.forEachChild(node, visitImports);
  };
  ts.forEachChild(sf, visitImports);
}

process.stderr.write(`TIMING total: ${((Date.now() - t0) / 1000).toFixed(1)}s\n`);
emit({
  computable: true,
  // Type-check only: no build scripts run, no plugins load, no target code
  // executes. Unlike the Rust helper, which must run build.rs to load a
  // workspace at all.
  executed_target_code: false,
  unresolved_call_sites: unresolved,
  // On the helper envelope only. magma does NOT forward this into the
  // artifact's `disclosure` — see the comment on rootsByRule.
  roots_by_rule: rootsByRule,
  functions,
  calls,
});
