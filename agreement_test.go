package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/robot-accomplice/magma/internal/architext"
	"github.com/robot-accomplice/magma/internal/contract"
)

// EVERY artifact describing one run must agree about that run.
//
// This drives the real `run` pipeline rather than assembling a graph, because
// the defect it pins was in main.go's ORDERING, not in any single function.
// Go passes the graph by value, `writeArtifacts` called Finalize on its own
// copy, and the architext emit was built from the caller's un-finalized graph:
// graph.json carried `disclosure` while code-graph.json did not. A limitation
// declaring `evidenced_by: "root_ratio"` then pointed at a field absent from
// the document, and architext's referential-integrity check refused it.
//
// The earlier TestWriteArtifactsAttachesDisclosure passed throughout, because
// it asserted that the function it was testing did its job — not that the
// artifacts a consumer reads agree. A green suite concealed the bug for the
// whole of v0.3.0.
func TestEveryArtifactAgreesAboutDisclosure(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"go.mod":  "module tmpmod\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() { Live() }\n\nfunc Live() {}\n\nfunc Dead() {}\n",
	})
	// The architext emit only fires when this directory already exists.
	if err := os.MkdirAll(filepath.Join(repo, "docs", "architext", "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	outRoot := t.TempDir()
	if err := run(repo, "proj", outRoot, runOpts{architext: true}); err != nil {
		t.Fatalf("run: %v", err)
	}

	dataDir := filepath.Join(outRoot, "proj", ".magma")
	for _, path := range []string{
		filepath.Join(dataDir, "graph.json"),
		filepath.Join(dataDir, architext.FileName),
		filepath.Join(repo, "docs", "architext", "data", architext.FileName),
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Limitations []struct {
				ID          string `json:"id"`
				EvidencedBy string `json:"evidenced_by"`
			} `json:"limitations"`
			Disclosure *contract.Disclosure `json:"disclosure"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		name := filepath.Base(path)
		if doc.Disclosure == nil {
			t.Errorf("%s: no disclosure object — artifacts disagree about the same run", name)
		}
		for _, l := range doc.Limitations {
			// A dangling evidenced_by is a claim whose supporting number never
			// arrives. Worst on an over-approximates-live limitation, whose
			// entire point is that an empty result is absence of evidence.
			if l.EvidencedBy != "" && doc.Disclosure == nil {
				t.Errorf("%s: limitation %q is evidenced_by %q but the document has no disclosure",
					name, l.ID, l.EvidencedBy)
			}
		}
	}
}
