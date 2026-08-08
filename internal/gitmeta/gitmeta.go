// Package gitmeta stamps the provenance every map carries: which commit it was
// computed at and whether the working tree was dirty. Backends never touch git;
// they receive a fully-formed contract.Meta.
package gitmeta

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/robot-accomplice/magma/internal/contract"
)

// Load reads the full HEAD sha and dirty state of the repo. Tree is the sha,
// suffixed "-dirty" when the working tree has uncommitted changes — so a map
// computed against a dirty tree is never mistaken for one pinned to a commit.
// Any repo-relative paths in ignore are excluded from the dirty check — magma
// passes the artifact it writes into the repo so its own output never makes
// the NEXT run see a dirty tree (which would flip the deterministic tree
// stamp and defeat freshness). A real source change is still reported dirty.
func Load(repo string, ignore ...string) (contract.Meta, error) {
	// FULL sha, not `--short`. With core.abbrev=auto git picks the abbreviation
	// width from the repo's object count, so it differs per repo (measured: 7
	// chars on magma, 8 on roboticus) AND GROWS for one repo as objects
	// accumulate. A provenance field whose width drifts over time cannot be
	// byte-identical across runs and cannot be compared literally by a
	// consumer — which is exactly how a consumer passing a full sha came to be
	// told "the map describes a different tree".
	sha, err := gitOut(repo, "rev-parse", "HEAD")
	if err != nil {
		return contract.Meta{}, err
	}
	status, err := gitOut(repo, append([]string{"status", "--porcelain"}, pathspec(ignore)...)...)
	if err != nil {
		return contract.Meta{}, err
	}
	tree, stamped := sha, sha
	if strings.TrimSpace(status) != "" {
		tree = sha + "-dirty"
		h, err := diffHash(repo, ignore)
		if err != nil {
			return contract.Meta{}, err
		}
		// `+` cannot occur in a hex sha and does not collide with the `-dirty`
		// convention on tree. `tree` deliberately keeps the BASE sha: one
		// identity field changing shape is easier to absorb than two.
		stamped = sha + "+" + h
	}
	date, _ := gitOut(repo, "show", "-s", "--format=%cI", "HEAD") // %cI = committer date, ISO-8601
	return contract.Meta{SHA: stamped, Tree: tree, CommitDate: date}, nil
}

// diffHashLen is how much of the digest is carried. 48 bits is far beyond what
// is needed to tell apart the working-tree states of one repo, and keeps the
// composite id readable.
const diffHashLen = 12

// diffHash fingerprints the working tree's CONTENT, so two dirty runs at the
// same commit mapping different code are distinguishable.
//
// A content hash rather than a wall-clock timestamp, deliberately: a timestamp
// would make two runs of an UNTOUCHED dirty tree produce different artifacts,
// breaking the byte-identical property this project verifies repeatedly and
// missing a consumer's cache on every run. This is deterministic for a given
// working-tree state, which is the property actually wanted.
//
// Two things it must get right, both easy to miss:
//
//   - It applies the SAME exclusions as the dirty check above. Without that,
//     magma's own artifact would move the hash on every run — reintroducing,
//     one layer down, the very bug `ignore` exists to prevent.
//   - It covers untracked file CONTENT, not just their names. `git diff HEAD`
//     ignores untracked files entirely and `git status --porcelain` lists them
//     without content, so a new untracked `.go` file — which WILL be compiled
//     into the map — would otherwise leave the hash unmoved. That is a silent
//     partial hash, the failure mode this project exists to avoid.
func diffHash(repo string, ignore []string) (string, error) {
	h := sha256.New()

	diff, err := gitRaw(repo, append([]string{"diff", "HEAD"}, pathspec(ignore)...)...)
	if err != nil {
		return "", err
	}
	h.Write(diff)

	raw, err := gitRaw(repo, append([]string{"ls-files", "-o", "--exclude-standard", "-z"}, pathspec(ignore)...)...)
	if err != nil {
		return "", err
	}
	names := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	sort.Strings(names) // git already sorts; pinned so the digest cannot depend on it
	for _, name := range names {
		if name == "" {
			continue
		}
		h.Write([]byte(name))
		body, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			// Vanished or unreadable between listing and reading. The name is
			// already hashed, so its presence still counts.
			continue
		}
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil))[:diffHashLen], nil
}

// pathspec renders the exclusion list git wants. Shared by the dirty check and
// the hash so the two can never drift apart — if they did, magma's own output
// would be invisible to one and visible to the other.
func pathspec(ignore []string) []string {
	if len(ignore) == 0 {
		return nil
	}
	out := []string{"--", "."}
	for _, p := range ignore {
		out = append(out, ":(exclude)"+p)
	}
	return out
}

// gitBin resolves the git executable ONCE to an absolute path.
//
// Two reasons, and the linter's is the lesser one. Resolving up front turns a
// missing git into a single clear error instead of an opaque exec failure on
// whichever call happened to run first. And it pins the binary for the whole
// run: without it every invocation re-consults PATH, so a PATH entry that is
// writable mid-run could substitute a different executable between calls
// (go:S4036).
//
// LookPath still consults PATH — this does not make a hostile PATH safe, and
// claiming otherwise would be the kind of security theatre this project avoids.
// What it does is make the resolution explicit, singular, and auditable.
var gitBin = sync.OnceValues(func() (string, error) {
	p, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git not found on PATH: %w", err)
	}
	return p, nil
})

// gitRaw returns git's stdout unmodified. Hashing needs the exact bytes:
// gitOut trims surrounding whitespace, which would erase a real difference
// between two working-tree states.
//
// This is the package's only process invocation; gitOut wraps it rather than
// duplicating it, so there is one place to harden rather than two that must be
// kept in step.
func gitRaw(repo string, args ...string) ([]byte, error) {
	bin, err := gitBin()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = repo
	return cmd.Output()
}

func gitOut(repo string, args ...string) (string, error) {
	out, err := gitRaw(repo, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
