package backend

import (
	"github.com/robot-accomplice/magma/internal/contract"

	"sort"
	"testing"
)

// Family E is the SINGLE-implementation interface: an abstraction with exactly
// one concrete implementor, where the indirection may not be earning its keep.
//
// The fixture encodes every judgement this file makes, so each exclusion is a
// case that would fail if the rule were dropped rather than a claim in a
// comment:
//
//   - Speaker  — one implementor            -> REPORTED
//   - PtrOnly  — one implementor, via *Impl -> REPORTED (pointer receivers count)
//   - Multi    — two implementors           -> not single-implementation
//   - Orphan   — zero implementors          -> a DIFFERENT finding, see below
//   - Empty    — interface{}                -> every type satisfies it
//   - Number   — a generic constraint       -> a type set, not a method set
//
// Zero implementors is deliberately NOT reported here. It is a dead
// abstraction, which is a different claim with a different remedy, and folding
// it in would make one row mean two things — the consuming gate treats every
// row in a file identically, so it could not tell them apart.
func TestInterfacesReportsOnlySingleImplementation(t *testing.T) {
	g := buildFixture(t, "ifacemod")

	var got []string
	for _, r := range g.Interfaces {
		got = append(got, r.Symbol)
	}
	sort.Strings(got)

	want := []string{"PtrOnly", "Speaker"}
	if len(got) != len(want) {
		t.Fatalf("interfaces = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interfaces = %v, want %v", got, want)
		}
	}
}

// A row must point at the interface's own declaration, or a reader cannot find
// the thing being reported.
func TestInterfaceRowsCarryADeclarationSite(t *testing.T) {
	g := buildFixture(t, "ifacemod")
	if len(g.Interfaces) == 0 {
		t.Fatal("no interfaces reported")
	}
	for _, r := range g.Interfaces {
		if r.File == "" || r.Line == 0 {
			t.Errorf("interface %q has no declaration site: file=%q line=%d", r.Symbol, r.File, r.Line)
		}
	}
}

// A module with no interfaces reports none, rather than reporting the
// dependency closure's. Scope is the module's own interfaces AND its own
// implementors: an interface implemented once locally but also by a dependency
// is not single-implementation, and neither is one magma cannot see all of.
func TestInterfacesAreModuleScoped(t *testing.T) {
	g := buildFixture(t, "livemod")
	for _, r := range g.Interfaces {
		if r.File == "" {
			t.Errorf("out-of-module interface leaked into the report: %q", r.Symbol)
		}
	}
}

// "Found none" and "cannot answer" are different claims. collectInterfaces
// returns a non-nil empty slice when it ran and found nothing, so a clean
// module reports a COMPUTED empty result rather than a refusal.
//
// Caught on magma itself: it has no single-implementation interfaces, and the
// first implementation returned nil, so _interfaces.json refused with
// "not implemented for this language" — reporting an analysed module as
// unanalysed. Absence reading as a result is the failure this project exists
// to avoid.
func TestNoSingleImplementationIsComputedNotRefused(t *testing.T) {
	g := buildFixture(t, "livemod")
	if g.Interfaces == nil {
		t.Fatal("Interfaces is nil after a successful Go analysis — a refusal, not an empty result")
	}
	n := g.InterfacesView(contract.Meta{})
	if !n.ReachabilityComputable {
		t.Errorf("view refused a module that was analysed: %q", n.NotComputableReason)
	}
	if n.Rows == nil {
		t.Error("computed note must carry [] rather than null rows")
	}
}
