package notes

import (
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestRenderProducesExpectedFiles(t *testing.T) {
	g := fixtureGraph() // module "ex"; pkg "ex/sub" -> rel "sub" -> nodes/sub/B.md
	files, manifest := Render(g,
		contract.Note{ReachabilityComputable: true, Rows: []contract.Row{{Symbol: "C", File: "c.go", Line: 3}}},
		contract.Note{ReachabilityComputable: true},
		Options{FolderName: "ex", Hotspots: 10, Validated: "t"})
	for _, want := range []string{"Overview.md", "Dead code.md", "Test-only code.md", "Packages.md", "nodes/sub/B.md", "nodes/main.md"} {
		if _, ok := files[want]; !ok {
			t.Errorf("Render missing file %q; got keys %v", want, keysOf(files))
		}
	}
	if len(manifest) != len(files) {
		t.Errorf("manifest (%d) must list every file (%d)", len(manifest), len(files))
	}
	// manifest is sorted
	for i := 1; i < len(manifest); i++ {
		if manifest[i-1] > manifest[i] {
			t.Errorf("manifest not sorted at %d: %q > %q", i, manifest[i-1], manifest[i])
		}
	}
}

func keysOf(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestReconcile(t *testing.T) {
	stale := Reconcile([]string{"a.md", "nodes/x/Old.md", "Overview.md"}, []string{"a.md", "Overview.md"})
	if len(stale) != 1 || stale[0] != "nodes/x/Old.md" {
		t.Errorf("Reconcile = %v, want [nodes/x/Old.md]", stale)
	}
}
