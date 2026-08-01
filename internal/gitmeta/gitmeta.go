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
// Any repo-relative paths in ignore are excluded from the dirty check — magma
// passes the artifact it writes into the repo so its own output never makes
// the NEXT run see a dirty tree (which would flip the deterministic tree
// stamp and defeat freshness). A real source change is still reported dirty.
func Load(repo string, ignore ...string) (contract.Meta, error) {
	sha, err := gitOut(repo, "rev-parse", "--short", "HEAD")
	if err != nil {
		return contract.Meta{}, err
	}
	statusArgs := []string{"status", "--porcelain"}
	if len(ignore) > 0 {
		statusArgs = append(statusArgs, "--", ".")
		for _, p := range ignore {
			statusArgs = append(statusArgs, ":(exclude)"+p)
		}
	}
	status, err := gitOut(repo, statusArgs...)
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
