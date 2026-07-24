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
	if meta.Tree != meta.SHA+"-dirty" {
		t.Errorf("dirty tree: Tree = %q, want %q", meta.Tree, meta.SHA+"-dirty")
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
