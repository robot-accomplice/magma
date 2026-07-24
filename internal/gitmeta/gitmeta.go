// Package gitmeta stamps the provenance every map carries: which commit it was
// computed at and whether the working tree was dirty. Backends never touch git;
// they receive a fully-formed contract.Meta.
package gitmeta

import (
	"os/exec"
	"strings"

	"github.com/robot-accomplice/magma/internal/contract"
)

// Load reads the short HEAD sha and dirty state of the repo. Tree is the sha,
// suffixed "-dirty" when the working tree has uncommitted changes — so a map
// computed against a dirty tree is never mistaken for one pinned to a commit.
func Load(repo string) (contract.Meta, error) {
	sha, err := gitOut(repo, "rev-parse", "--short", "HEAD")
	if err != nil {
		return contract.Meta{}, err
	}
	status, err := gitOut(repo, "status", "--porcelain")
	if err != nil {
		return contract.Meta{}, err
	}
	tree := sha
	if strings.TrimSpace(status) != "" {
		tree = sha + "-dirty"
	}
	date, _ := gitOut(repo, "show", "-s", "--format=%cI", "HEAD") // %cI = committer date, ISO-8601
	return contract.Meta{SHA: sha, Tree: tree, CommitDate: date}, nil
}

func gitOut(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
