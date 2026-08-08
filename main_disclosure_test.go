package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

// version must be bare semver, because release.yml compares it to the tag with
// the leading "v" stripped and hard-fails on a mismatch — in public, after the
// tag already exists.
//
// Deliberately a SHAPE check, not an equality check against a literal. This
// test previously pinned "0.3.0" and so had to be hand-edited every release:
// the same recurring-manual-step smell as the hardcoded supported-language
// list and the README's literal tag example. The real tag-vs-binary comparison
// lives in release.yml and cannot go stale; duplicating it here with a
// hardcoded string was strictly worse than checking the property.
func TestVersionIsBareSemver(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(version) {
		t.Fatalf("version = %q, want bare semver like 1.2.3 (no leading v, no suffix)", version)
	}
}

// writeArtifacts carries through the disclosure it is GIVEN. It deliberately
// does not Finalize: the graph arrives by value, so finalizing here would
// attach the disclosure to this function's own copy and leave the caller's
// other writers — notably the architext emit — describing a different run.
// That is the defect this ordering was changed to fix; see
// TestEveryArtifactAgreesAboutDisclosure, which pins the property rather than
// this plumbing.
func TestWriteArtifactsCarriesDisclosureThrough(t *testing.T) {
	dir := t.TempDir()
	g := contract.Graph{
		Computable: true,
		Fidelity:   "rta",
		Nodes:      []contract.Node{{ID: 1, Root: true}, {ID: 2}},
	}.Finalize() // the caller's job, as in emitReport
	if _, _, err := writeArtifacts(dir, contract.Meta{Generator: "magma/0.3.1"}, g); err != nil {
		t.Fatalf("writeArtifacts: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "graph.json"))
	if err != nil {
		t.Fatalf("read graph.json: %v", err)
	}
	var got contract.Graph
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal graph.json: %v", err)
	}
	if got.Disclosure == nil {
		t.Fatal("graph.json lost the disclosure it was given")
	}
	if got.Disclosure.RootRatio != 0.5 {
		t.Errorf("root_ratio = %v, want 0.5", got.Disclosure.RootRatio)
	}
}

// The row files must carry limitations, since a consumer reading _dead.json
// may never open graph.json.
func TestWriteArtifactsRowFilesCarryLimitations(t *testing.T) {
	dir := t.TempDir()
	g := contract.Graph{
		Computable:  true,
		Fidelity:    "rta",
		Limitations: contract.Limitations{{ID: "go-closure-edges", Effect: contract.EffectMayOmitEdges}},
		Nodes:       []contract.Node{{ID: 1, Root: true}, {ID: 2}},
	}
	if _, _, err := writeArtifacts(dir, contract.Meta{}, g); err != nil {
		t.Fatalf("writeArtifacts: %v", err)
	}
	for _, name := range []string{"_dead.json", "_test-only.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var n contract.Note
		if err := json.Unmarshal(b, &n); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		if len(n.Limitations) != 1 || n.Limitations[0].ID != "go-closure-edges" {
			t.Errorf("%s carries no limitations: %+v", name, n.Limitations)
		}
	}
}
