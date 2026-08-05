package gitmeta

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A clean working tree: Tree equals the short SHA, with no -dirty suffix.
func TestLoadCleanTree(t *testing.T) {
	repo := initRepo(t)
	meta, err := Load(repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if meta.SHA == "" {
		t.Fatal("SHA is empty")
	}
	if meta.Tree != meta.SHA {
		t.Errorf("clean tree: Tree = %q, want == SHA %q (no -dirty)", meta.Tree, meta.SHA)
	}
}

// An uncommitted change flips Tree to SHA+"-dirty" so a dirty map is never
// mistaken for one pinned to a commit.
func TestLoadDirtyTree(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, err := Load(repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Tree keeps the BASE sha; SHA carries the +diffhash. Deliberate: one
	// identity field changing shape is easier for a consumer to absorb than
	// two, and `tree`'s job is only the clean/dirty bit.
	base, _, _ := strings.Cut(meta.SHA, "+")
	if meta.Tree != base+"-dirty" {
		t.Errorf("dirty tree: Tree = %q, want %q", meta.Tree, base+"-dirty")
	}
}

// Outside a git repo, Load surfaces the error rather than fabricating provenance.
func TestLoadNotAGitRepo(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("Load in a non-git dir must return an error")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init")
	run(t, repo, "git", "config", "user.email", "test@example.com")
	run(t, repo, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-m", "init")
	return repo
}

func TestLoadCommitDate(t *testing.T) {
	repo := initRepo(t)
	meta, err := Load(repo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.HasPrefix(meta.CommitDate, "2020-01-01") {
		t.Errorf("CommitDate = %q, want it to start 2020-01-01", meta.CommitDate)
	}
}

// The generated artifact magma writes into a target repo must not itself flip
// the tree stamp to dirty on the next run (that would defeat freshness and
// break determinism) — but a real source change must still register as dirty
// even with the artifact ignored.
func TestLoadIgnoresGeneratedArtifact(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = repo
		if err := c.Run(); err != nil {
			t.Fatal(err)
		}
	}
	run("init")
	run("config", "user.email", "t@t.t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.go")
	run("commit", "-m", "init")

	// Untracked generated artifact present.
	if err := os.MkdirAll(filepath.Join(repo, "docs/architext/data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs/architext/data/code-graph.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty, err := Load(repo) // no ignore -> sees the untracked artifact
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dirty.Tree, "-dirty") {
		t.Errorf("without ignore, tree = %q, want -dirty", dirty.Tree)
	}
	clean, err := Load(repo, "docs/architext/data/code-graph.json") // ignore it
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(clean.Tree, "-dirty") {
		t.Errorf("with the artifact ignored, tree = %q, want clean", clean.Tree)
	}
	// A REAL change must still register as dirty even with the ignore.
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package m\n// x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stillDirty, err := Load(repo, "docs/architext/data/code-graph.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stillDirty.Tree, "-dirty") {
		t.Errorf("a real source change must be dirty despite the ignore; tree = %q", stillDirty.Tree)
	}
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Keep commits deterministic and independent of the host's git identity.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
