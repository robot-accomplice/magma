// Package architext maps magma's in-memory call graph to the machine-authored
// magma-code-graph/1 artifact Architext ingests. It is an outer-ring output
// adapter: it depends on internal/contract; nothing in contract or the backends
// depends on it. The dependency arrow points inward only.
package architext

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/robot-accomplice/magma/internal/contract"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug lowercases s and collapses every run of non-[a-z0-9] into a single "-",
// trimming leading/trailing dashes. Deterministic and dependency-free.
func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// moduleID is the stable slug for a package.
func moduleID(pkg string) string { return slug(pkg) }

// assignIDs returns a stable slug for every node ID, keyed by node.ID. The base
// slug is slug(pkg + "-" + symbol). Collisions (e.g. value + pointer methods on
// one type) are broken deterministically by (file, line, id): the node earliest
// by (file, line) keeps the base slug; the next gets "-2", then "-3", and so on.
// id is the final tiebreak — it is unique per node and assigned upstream in
// collectNodes independent of assignIDs's input order, so (file, line, id) is a
// total order and the result is independent of the order nodes was passed in.
func assignIDs(nodes []contract.Node) map[int]string {
	byBase := map[string][]contract.Node{}
	for _, n := range nodes {
		base := slug(n.Pkg + "-" + n.Symbol)
		byBase[base] = append(byBase[base], n)
	}
	out := make(map[int]string, len(nodes))
	for base, group := range byBase {
		sort.Slice(group, func(i, j int) bool {
			if group[i].File != group[j].File {
				return group[i].File < group[j].File
			}
			if group[i].Line != group[j].Line {
				return group[i].Line < group[j].Line
			}
			return group[i].ID < group[j].ID
		})
		for i, n := range group {
			if i == 0 {
				out[n.ID] = base
			} else {
				out[n.ID] = base + "-" + strconv.Itoa(i+1)
			}
		}
	}
	return out
}
