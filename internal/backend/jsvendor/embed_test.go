package jsvendor

import (
	"os"
	"path/filepath"
	"testing"
)

// The compiler and the standard-library types must both extract. The lib files
// are NOT optional: without them the checker cannot resolve Array, Promise or
// console, and every global goes unresolved.
func TestExtractWritesCompilerAndLibs(t *testing.T) {
	dir := t.TempDir()
	if err := Extract(dir); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "typescript.js"))
	if err != nil {
		t.Fatalf("typescript.js not extracted: %v", err)
	}
	if fi.Size() < 5_000_000 {
		t.Errorf("typescript.js is %d bytes — too small to be the real compiler", fi.Size())
	}
	libs, err := filepath.Glob(filepath.Join(dir, "lib", "*.d.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) < 100 {
		t.Errorf("extracted %d lib/*.d.ts files, want all 102", len(libs))
	}
}

// Extraction must be idempotent: magma is designed to run constantly — before
// every task, in CI, as a git hook — so re-writing 12.5 MB each time would be a
// visible cost for no benefit.
func TestExtractIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Extract(dir); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "typescript.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Extract(dir); err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	after, err := os.Stat(filepath.Join(dir, "typescript.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("Extract rewrote an already-extracted file")
	}
}

func TestVersionIsThePin(t *testing.T) {
	if Version != "5.9.3" {
		t.Fatalf("Version = %q, want 5.9.3 — the pin is load-bearing, see VENDOR.md", Version)
	}
}
