package jsdriver

import (
	"os"
	"path/filepath"
	"testing"
)

// write lays out a repo from a path->content map, creating directories.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for name, body := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func rootsOf(env envelope) map[string]bool {
	out := map[string]bool{}
	for _, f := range env.Functions {
		if f.Root {
			out[f.File+":"+f.Symbol] = true
		}
	}
	return out
}

// THE LOAD-BEARING NEGATIVE, and the one most likely to regress silently.
//
// In JavaScript nearly everything is exported, so rooting every export
// reproduces the measured Bevy failure exactly: 87% of nodes rooted, ZERO dead
// functions reported out of 156, and no discriminating power left. An internal
// module's export earns liveness by being IMPORTED, not by being exported.
//
// If this test ever goes green by rooting everything, the tool still "works"
// and reports nothing — which is why the assertion is on the ratio as well as
// on the symbol.
func TestOrdinaryExportIsNotARoot(t *testing.T) {
	repo := write(t, map[string]string{
		"package.json": `{"name":"p","version":"1.0.0","main":"src/index.js"}`,
		"src/index.js": "import { used } from './lib.js';\nexport function start() { used(); }\nstart();\n",
		"src/lib.js":   "export function used() {}\nexport function neverImported() {}\n",
	})
	env := run(t, repo)
	roots := rootsOf(env)

	if roots["src/lib.js:neverImported"] {
		t.Error("an ordinary module's export was rooted; in JS that roots almost everything and destroys the analysis")
	}
	if roots["src/lib.js:used"] {
		t.Error("an ordinary module's export was rooted even though it is only reached by an import")
	}
	if !roots["src/index.js:start"] {
		t.Error("the declared package main's export is an entry point and must be rooted")
	}

	var total, rooted int
	for _, f := range env.Functions {
		total++
		if f.Root {
			rooted++
		}
	}
	if total > 0 && float64(rooted)/float64(total) > 0.5 {
		t.Errorf("root ratio %d/%d — over-rooting is how this analysis dies quietly", rooted, total)
	}
}

// Each root class named in the design, proven to be the thing that roots the
// file. One fixture per class, so a broken rule fails as itself rather than
// hiding behind another rule that happened to claim the same file.
func TestEachRootClassRootsItsOwnEntryPoint(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string // file:symbol that must be a root
		rule  string // the rule id that must have claimed it
	}{
		{
			name: "declared package main",
			files: map[string]string{
				"package.json": `{"name":"p","version":"1.0.0","main":"src/entry.js"}`,
				"src/entry.js": "export function boot() {}\n",
			},
			want: "src/entry.js:boot", rule: "pkg-main",
		},
		{
			name: "package bin",
			files: map[string]string{
				"package.json": `{"name":"p","version":"1.0.0","bin":{"tool":"cli/main.js"}}`,
				"cli/main.js":  "export function cli() {}\n",
			},
			want: "cli/main.js:cli", rule: "pkg-bin",
		},
		{
			name: "package exports, nested conditions",
			files: map[string]string{
				"package.json": `{"name":"p","version":"1.0.0","exports":{".":{"import":"./src/api.js"}}}`,
				"src/api.js":   "export function api() {}\n",
			},
			want: "src/api.js:api", rule: "pkg-exports",
		},
		{
			name: "script target",
			files: map[string]string{
				"package.json": `{"name":"p","version":"1.0.0","scripts":{"start":"node server.js"}}`,
				"server.js":    "export function serve() {}\n",
			},
			want: "server.js:serve", rule: "pkg-scripts",
		},
		{
			name: "next app router page",
			files: map[string]string{
				"package.json":      `{"name":"p","version":"1.0.0"}`,
				"app/home/page.tsx": "export default function Page() { return null; }\n",
			},
			want: "app/home/page.tsx:Page", rule: "next-app-router",
		},
		{
			name: "middleware",
			files: map[string]string{
				"package.json":  `{"name":"p","version":"1.0.0"}`,
				"middleware.ts": "export function middleware() {}\n",
			},
			want: "middleware.ts:middleware", rule: "next-middleware",
		},
		{
			name: "test entry",
			files: map[string]string{
				"package.json": `{"name":"p","version":"1.0.0"}`,
				"a.test.ts":    "export function checks() {}\n",
			},
			want: "a.test.ts:checks", rule: "test-file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := run(t, write(t, tc.files))
			if !rootsOf(env)[tc.want] {
				t.Errorf("%s is an entry point of class %q but was not rooted; roots=%v",
					tc.want, tc.rule, rootsOf(env))
			}
			if env.RootsByRule[tc.rule] == 0 {
				t.Errorf("roots_by_rule has no count for %q: %v", tc.rule, env.RootsByRule)
			}
		})
	}
}

// A default export bound through a const carries no Export modifier on the
// function itself, and it is one of the two common ways a Next.js page is
// written. Reading modifier flags instead of asking the checker leaves it
// unrooted and reports the page DEAD — a deletion order for the page.
func TestConstBoundDefaultExportIsRooted(t *testing.T) {
	repo := write(t, map[string]string{
		"package.json": `{"name":"p","version":"1.0.0"}`,
		"app/page.tsx": "const Page = () => null;\nexport default Page;\n",
	})
	if !rootsOf(run(t, repo))["app/page.tsx:Page"] {
		t.Error("a const-bound default export was not rooted; the page would report dead")
	}
}

// A test file's module scope is a root, but only via a TEST rule — so it seeds
// reachability without seeding PRODUCTION reachability. Getting this wrong in
// either direction is a known, measured failure: seed too broadly and a dead
// test helper can never be reported; seed only on production roots and all
// test-only code reads dead.
func TestTestEntriesAreTestRootsNotProductionRoots(t *testing.T) {
	repo := write(t, map[string]string{
		"package.json": `{"name":"p","version":"1.0.0"}`,
		"a.test.ts":    "function helper() {}\nhelper();\n",
	})
	env := run(t, repo)
	var sawTestRoot bool
	for _, f := range env.Functions {
		if f.Root && !f.Test {
			t.Errorf("%s:%s is a PRODUCTION root, but it comes from a test file", f.File, f.Symbol)
		}
		if f.Root && f.Test {
			sawTestRoot = true
		}
		// The helper is declared in a test file, so it is test-flagged — but it
		// must NOT be a root, or a dead test helper could never be reported.
		if f.Symbol == "helper" && f.Root {
			t.Error("a function merely declared in a test file was rooted")
		}
	}
	if !sawTestRoot {
		t.Error("a test file produced no test root at all; _test-only can never mean anything")
	}
}

// `<Foo />` invokes Foo. Without this every React component has no incoming
// edge and reports dead — the analogue of the Rust family-A defect where Bevy
// systems passed as values had no syntactic call site.
func TestJSXElementIsACall(t *testing.T) {
	repo := write(t, map[string]string{
		"package.json":  `{"name":"p","version":"1.0.0"}`,
		"app/page.tsx":  "import { Widget } from '../ui/widget';\nexport default function Page() { return <Widget />; }\n",
		"ui/widget.tsx": "export function Widget() { return null; }\n",
	})
	env := run(t, repo)

	var widget = -1
	for _, f := range env.Functions {
		if f.Symbol == "Widget" && f.File == "ui/widget.tsx" {
			widget = f.ID
		}
	}
	if widget < 0 {
		t.Fatalf("Widget was not collected: %+v", env.Functions)
	}
	for _, c := range env.Calls {
		if c.To != nil && *c.To == widget {
			return
		}
	}
	t.Error("no edge into Widget; a component used only as <Widget /> would report dead")
}

// An intrinsic element is not a call into user code. Counting it would inflate
// unresolved_call_sites on every React repo and make the number meaningless
// exactly where it is most needed.
func TestIntrinsicJSXIsNotCountedUnresolved(t *testing.T) {
	repo := write(t, map[string]string{
		"package.json": `{"name":"p","version":"1.0.0"}`,
		"app/page.tsx": "export default function Page() { return <div><span /></div>; }\n",
	})
	if got := run(t, repo).UnresolvedCallSites; got != 0 {
		t.Errorf("unresolved_call_sites = %d, want 0: <div> and <span> are intrinsics, not unresolved calls", got)
	}
}

// A function handed to something else is invoked by whatever received it.
//
// Every case here was a REAL false dead measured on a production Next.js repo,
// where 659 of 750 nodes reported dead before these were modelled. That number
// is not a quality problem, it is a safety one: magma's dead set is a deletion
// order, so mass false-dead is the most damaging way this tool can fail.
//
// Each case is a distinct syntactic shape, because each failed independently.
func TestFunctionsHandedOffAsValuesAreReachable(t *testing.T) {
	cases := []struct {
		name, body, symbol string
	}{
		{
			name:   "inline callback argument",
			body:   "export function go(xs){ return xs.map(function inline(x){ return x; }); }\ngo([]);\n",
			symbol: "inline",
		},
		{
			name:   "named function passed by reference",
			body:   "function onTick(){}\nexport function go(){ setTimeout(onTick, 1); }\ngo();\n",
			symbol: "onTick",
		},
		{
			name:   "function inside an options object",
			body:   "function onDone(){}\nexport function go(){ run({ onDone }); }\nfunction run(o){ return o; }\ngo();\n",
			symbol: "onDone",
		},
		{
			name:   "function chosen by a conditional",
			body:   "function a(){}\nfunction b(){}\nexport function go(f){ return pick(f ? a : b); }\nfunction pick(x){ return x; }\ngo(true);\n",
			symbol: "a",
		},
		{
			name:   "returned closure, as every useEffect cleanup is written",
			body:   "export function go(){ return function cleanup(){}; }\ngo();\n",
			symbol: "cleanup",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := write(t, map[string]string{
				"package.json": `{"name":"p","version":"1.0.0","main":"index.js"}`,
				"index.js":     tc.body,
			})
			env := run(t, repo)

			target := -1
			for _, f := range env.Functions {
				if f.Symbol == tc.symbol {
					target = f.ID
				}
			}
			if target < 0 {
				t.Fatalf("%s was not collected: %+v", tc.symbol, env.Functions)
			}
			for _, c := range env.Calls {
				if c.To != nil && *c.To == target {
					return
				}
			}
			t.Errorf("no edge into %s; it is handed off as a value and would report DEAD — a deletion order for live code", tc.symbol)
		})
	}
}

// Build output is not source. A Next.js tsconfig includes .next/types/*.ts, so
// filtering only the directory scan misses them — measured: .next/types/
// validator.ts was collected and then reported dead, a deletion order for a
// file the build regenerates.
func TestBuildOutputIsNotAnalysed(t *testing.T) {
	repo := write(t, map[string]string{
		"package.json":       `{"name":"p","version":"1.0.0","main":"index.js"}`,
		"index.js":           "export function go(){}\n",
		".next/types/gen.ts": "export function generated(){}\n",
		"dist/bundle.js":     "export function bundled(){}\n",
	})
	for _, f := range run(t, repo).Functions {
		if f.Symbol == "generated" || f.Symbol == "bundled" {
			t.Errorf("%s in %s is build output and must not be analysed", f.Symbol, f.File)
		}
	}
}
