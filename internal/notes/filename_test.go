package notes

import (
	"strings"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

// maxComponent is the per-component byte limit every filesystem magma targets
// honours. The note path must respect it INCLUDING the ".md" suffix.
const maxComponent = 255

// A Rust generic can produce a symbol far past the filesystem's per-component
// limit. Before this fix magma aborted mid-run with "file name too long":
// graph.json still landed, but the Obsidian map was silently truncated at that
// node and code-graph.json was never dual-written. Partial output that looks
// complete is the exact failure this project exists to prevent.
func TestNotePathBoundedForLongRustGeneric(t *testing.T) {
	sym := "DiagramSvgPropsBuilder<((Plan,), (Option<Flow>,), " +
		strings.Repeat("(HashMap<String, String, RandomState, Global>,), ", 8) +
		"(View,), __drillable_node_ids, __selected_node, __on_select)>::build"
	if len(sym) <= maxComponent {
		t.Fatalf("fixture symbol is only %d bytes; it must exceed %d to test the bug", len(sym), maxComponent)
	}
	n := contract.Node{Symbol: sym, Pkg: "architext_viewer::diagram::svg"}
	for _, seg := range strings.Split(notePath(n, ""), "/") {
		if len(seg) > maxComponent {
			t.Errorf("path component is %d bytes, over the %d limit: %q", len(seg), maxComponent, seg)
		}
	}
}

// Windows forbids < > : " | ? * in filenames. Rust symbols carry them
// structurally, not occasionally: on roboticus-rust, 3,383 of 10,921 symbols
// contain ':' (every Type::method) and 2,104 contain '<' and '>' (every
// <T as Trait>::method). magma ships a Windows binary, so this broke ~31% of
// notes for any Windows user mapping a Rust repo. The Go backend emits none of
// these characters, which is why it went unnoticed.
func TestNotePathHasNoWindowsIllegalChars(t *testing.T) {
	for _, sym := range []string{
		"<CompositionResult as Debug>::fmt",
		"<MultimodalConfig as Default>::default",
		"CompositionResult::is_complete",
	} {
		n := contract.Node{Symbol: sym, Pkg: "roboticus_core::composition"}
		got := notePath(n, "")
		if i := strings.IndexAny(got, `<>:"|?*`); i >= 0 {
			t.Errorf("notePath(%q) = %q contains illegal char %q", sym, got, got[i])
		}
	}
}

// Sanitising and truncating both destroy information, so distinct symbols must
// not be allowed to land on the same file. A silent merge would be worse than
// the abort it replaces: two functions' notes would become one, and nothing
// would say so.
func TestDistinctSymbolsNeverShareANotePath(t *testing.T) {
	long := strings.Repeat("Wrapper<", 40) + "T" + strings.Repeat(">", 40)
	syms := []string{
		"A::b", "A<b", "A>b", "A:b", // all collapse to the same sanitised stem
		"<X as Trait>::run", "<Y as Trait>::run",
		long + "::alpha", long + "::beta", // differ only past the truncation point
	}
	seen := map[string]string{}
	for _, s := range syms {
		p := notePath(contract.Node{Symbol: s, Pkg: "p"}, "")
		if prev, dup := seen[p]; dup {
			t.Errorf("collision: %q and %q both map to %q", prev, s, p)
		}
		seen[p] = s
	}
}

// Go symbols are already safe and short, so the fix must be a no-op for them.
// Otherwise every existing Go map churns its entire notes/ tree on the next
// run for no reason.
func TestGoSymbolsAreUnchanged(t *testing.T) {
	n := contract.Node{Symbol: "BuildGraph", Pkg: "github.com/x/m/internal/backend"}
	if got := notePath(n, "github.com/x/m"); got != "nodes/internal/backend/BuildGraph.md" {
		t.Errorf("Go notePath changed: %q", got)
	}
	if got := wikiTarget(n, "github.com/x/m"); got != "internal/backend/BuildGraph" {
		t.Errorf("Go wikiTarget changed: %q", got)
	}
}

// The link target and the file on disk must agree, or every [[wikilink]] in the
// map dangles.
func TestWikiTargetMatchesNotePath(t *testing.T) {
	for _, sym := range []string{
		"BuildGraph",
		"<CompositionResult as Debug>::fmt",
		strings.Repeat("Long<", 60) + "T" + strings.Repeat(">", 60),
	} {
		n := contract.Node{Symbol: sym, Pkg: "pkg"}
		if want, got := "nodes/"+wikiTarget(n, "")+".md", notePath(n, ""); want != got {
			t.Errorf("wikiTarget/notePath disagree for %q: %q vs %q", sym, want, got)
		}
	}
}
