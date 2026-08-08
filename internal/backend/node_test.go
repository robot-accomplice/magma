package backend

import (
	"os/exec"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
)

func TestNodeBackendRegistered(t *testing.T) {
	b, ok := For(detect.Node)
	if !ok {
		t.Fatal("no backend registered for detect.Node")
	}
	if b.Language() != "node" {
		t.Errorf("Language() = %q, want node", b.Language())
	}
}

// A backend that declares no limitations reads as UNLIMITED, which is the
// "unlimited by omission" failure the field exists to prevent. A NEW backend is
// exactly where that omission is easiest to make.
func TestNodeBackendDeclaresLimitations(t *testing.T) {
	ls := nodeLimitations()
	if len(ls) == 0 {
		t.Fatal("the node backend declares no limitations")
	}
	for _, l := range ls {
		if l.ID == "" || l.Attribution == "" || l.Description == "" || l.Effect == "" {
			t.Errorf("incomplete limitation: %+v", l)
		}
	}
}

// The dynamic-call limitation must point at the count that quantifies it, or a
// consumer cannot tell HOW MUCH it bit on this run.
func TestDynamicCallLimitationIsEvidenced(t *testing.T) {
	for _, l := range nodeLimitations() {
		if l.ID == "js-dynamic-call-sites" {
			if l.EvidencedBy != "unresolved_call_sites" {
				t.Errorf("EvidencedBy = %q, want unresolved_call_sites", l.EvidencedBy)
			}
			if l.Scope != contract.ScopeLanguage {
				t.Errorf("scope = %q, want %q — it is JavaScript's nature, not magma's to-do",
					l.Scope, contract.ScopeLanguage)
			}
			return
		}
	}
	t.Fatal("js-dynamic-call-sites not declared")
}

// A missing runtime is a REFUSAL with an actionable reason, not an error and
// not a silent skip: a repo magma cannot map honestly gets an honest refusal,
// exactly as an unsupported language does.
func TestMissingNodeRefusesWithAttribution(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("MAGMA_JS_HELPER_NODE", "")
	b, _ := For(detect.Node)
	g, err := b.BuildGraph(t.TempDir(), contract.Meta{}, nil)
	if err != nil {
		t.Fatalf("a missing runtime must refuse, not error: %v", err)
	}
	if g.Computable {
		t.Fatal("expected a refusal")
	}
	if g.NotComputableReason == "" {
		t.Error("refusal carries no reason")
	}
	if len(g.Limitations) == 0 {
		t.Error("a refusal must still declare limitations — that is when a consumer most needs them")
	}
}

func TestNodeBackendBuildsAGraph(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	b, _ := For(detect.Node)
	g, err := b.BuildGraph("testdata/jsmod", contract.Meta{Generator: "magma/test"}, nil)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if !g.Computable {
		t.Fatalf("refused: %s", g.NotComputableReason)
	}
	if g.Fidelity != "semantic" {
		t.Errorf("fidelity = %q, want semantic", g.Fidelity)
	}
	if g.ExecutedTargetCode {
		t.Error("the JS backend type-checks only; it must not claim to have executed target code")
	}
	if len(g.Nodes) == 0 {
		t.Fatal("no nodes")
	}
	if len(g.Edges) == 0 {
		t.Error("no edges; the fixture's live() calls helper()")
	}
}
