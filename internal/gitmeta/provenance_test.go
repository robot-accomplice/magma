package gitmeta

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// `rev-parse --short` picks its width from the repo's object count
// (core.abbrev=auto), so it varies per repo AND grows for the same repo as
// objects accumulate — measured 2026-08-05 at 7 chars on magma and 8 on
// roboticus. A provenance field whose width drifts over time cannot be
// byte-identical across runs, and cannot be compared literally by a consumer.
func TestCleanTreeStampsFullSHA(t *testing.T) {
	meta, err := Load(initRepo(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !fullSHA.MatchString(meta.SHA) {
		t.Errorf("SHA = %q, want a full 40-char hex sha", meta.SHA)
	}
	if meta.Tree != meta.SHA {
		t.Errorf("clean tree: Tree = %q, want == SHA", meta.Tree)
	}
	if strings.Contains(meta.SHA, "+") {
		t.Errorf("clean tree must carry no +diffhash, got %q", meta.SHA)
	}
}

// A dirty map was built from HEAD *plus* uncommitted changes, so pinning to a
// bare sha records a boundary that cannot be returned to. The composite fails
// CLOSED at use time: a consumer that tries to resolve it errors rather than
// silently recording an unreachable commit.
func TestDirtyTreeStampsCompositeSHA(t *testing.T) {
	repo := initRepo(t)
	write(t, repo, "f.txt", "modified")

	meta, err := Load(repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	base, hash, ok := strings.Cut(meta.SHA, "+")
	if !ok {
		t.Fatalf("dirty tree SHA = %q, want <sha>+<diffhash>", meta.SHA)
	}
	if !fullSHA.MatchString(base) {
		t.Errorf("composite base = %q, want a full sha", base)
	}
	if hash == "" {
		t.Error("composite carries an empty diffhash")
	}
	// tree keeps the BASE sha: one identity field changing shape is easier for
	// a consumer to absorb than two.
	if meta.Tree != base+"-dirty" {
		t.Errorf("Tree = %q, want %q", meta.Tree, base+"-dirty")
	}
}

// The whole point: two dirty runs at the SAME commit mapping DIFFERENT code
// must be distinguishable. Architext's layout cache is keyed on
// (sha, tree, tier), and they reproduced the collision — an aliased shorter
// entry indexes cached positions by node index and panics, which in wasm traps
// the entire instance.
func TestDifferentDirtyContentGivesDifferentSHA(t *testing.T) {
	repo := initRepo(t)

	write(t, repo, "f.txt", "one")
	a, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	write(t, repo, "f.txt", "two")
	b, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA == b.SHA {
		t.Fatalf("two different dirty trees share SHA %q", a.SHA)
	}
	if a.Tree != b.Tree {
		t.Errorf("Tree should still be identical (same commit, both dirty): %q vs %q", a.Tree, b.Tree)
	}
}

// Deterministic: an UNTOUCHED dirty tree must stamp identically twice. This is
// why the design uses a content hash rather than a run timestamp — a timestamp
// would break magma's byte-identical property and miss a consumer's cache on
// every single run.
func TestSameDirtyContentIsDeterministic(t *testing.T) {
	repo := initRepo(t)
	write(t, repo, "f.txt", "stable")

	a, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA != b.SHA {
		t.Errorf("same dirty tree stamped differently: %q vs %q", a.SHA, b.SHA)
	}
}

// TRAP 1. `ignore` exists so magma's OWN output never makes the next run see a
// dirty tree. If the hash did not apply the identical exclusions, magma's
// writes would move the hash on every run — reintroducing that exact bug one
// layer down, where it is harder to see.
func TestIgnoredPathsDoNotMoveTheHash(t *testing.T) {
	repo := initRepo(t)
	write(t, repo, "f.txt", "dirty")
	before, err := Load(repo, "artifact.json")
	if err != nil {
		t.Fatal(err)
	}

	write(t, repo, "artifact.json", `{"magma":"own output"}`)
	after, err := Load(repo, "artifact.json")
	if err != nil {
		t.Fatal(err)
	}
	if before.SHA != after.SHA {
		t.Errorf("an ignored path moved the hash: %q -> %q", before.SHA, after.SHA)
	}
}

// TRAP 2. `git diff HEAD` does not cover untracked files at all, and
// `git status --porcelain` names them WITHOUT their content. A new untracked
// .go file WILL be compiled into the map, so its content must move the hash —
// otherwise two genuinely different maps stamp identically.
func TestUntrackedFileContentMovesTheHash(t *testing.T) {
	repo := initRepo(t)
	write(t, repo, "extra.go", "package p // version one")
	a, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}

	write(t, repo, "extra.go", "package p // version two")
	b, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA == b.SHA {
		t.Errorf("untracked file CONTENT did not move the hash; both stamped %q", a.SHA)
	}
}

func write(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
