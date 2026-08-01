// Package contract defines the codemap-rows/1 wire format that magma emits and
// downstream audit tooling (e.g. the slop sweep's gate) consumes.
//
// The format is deliberately dumb: one JSON file per relationship class
// (_dead.json, _test-only.json, ...), each a self-describing envelope carrying
// enough provenance for a consumer to decide how much to trust it — which SHA
// it was computed at, whether the working tree was dirty, what "dead" even means
// for this language (fidelity), and whether the analysis was computable at all.
package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Version is the wire-format version. Bump only on a breaking change to the
// envelope; consumers gate on this.
const Version = "codemap-rows/1"

// Row is a single symbol the analysis points at (a dead function, a test-only
// function, ...). File is repo-relative; Line is 1-based.
type Row struct {
	Symbol string `json:"symbol"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// Note is one relationship-class file. Rows is a nil slice when the analysis
// could not be computed — which marshals to JSON `null`, distinct from `[]`
// (computed, and genuinely empty). That distinction is load-bearing: a refusal
// must never be mistaken for "found nothing".
type Note struct {
	ContractVersion        string `json:"contract_version"`
	Generator              string `json:"generator"`
	SHA                    string `json:"sha"`
	Tree                   string `json:"tree"`
	Fidelity               string `json:"fidelity"`
	ReachabilityComputable bool   `json:"reachability_computable"`
	NotComputableReason    string `json:"not_computable_reason,omitempty"`
	Rows                   []Row  `json:"rows"`
}

// Meta is the provenance every Note in a run shares: it is stamped once by the
// runner and handed to each backend so backends never touch git or clocks.
type Meta struct {
	Generator  string // e.g. "magma/0.2.0"
	SHA        string // short HEAD sha
	Tree       string // SHA, or SHA+"-dirty" when the working tree is dirty
	CommitDate string // ISO-8601 commit date of HEAD (e.g. 2026-07-24T14:06:00-04:00), deterministic for a SHA
	Fidelity   string // per-language meaning of "dead" (backend-supplied)
}

// Computed builds a note whose analysis succeeded. Rows are sorted for a stable,
// diffable output; an empty (non-nil) slice stays `[]`, never `null`.
func (m Meta) Computed(rows []Row) Note {
	if rows == nil {
		rows = []Row{}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].File != rows[j].File {
			return rows[i].File < rows[j].File
		}
		if rows[i].Line != rows[j].Line {
			return rows[i].Line < rows[j].Line
		}
		return rows[i].Symbol < rows[j].Symbol
	})
	return Note{
		ContractVersion:        Version,
		Generator:              m.Generator,
		SHA:                    m.SHA,
		Tree:                   m.Tree,
		Fidelity:               m.Fidelity,
		ReachabilityComputable: true,
		Rows:                   rows,
	}
}

// Refused builds a note whose analysis could not be computed. Rows stays nil
// (marshals to `null`); reason explains why so a consumer can surface it instead
// of silently treating the class as empty.
func (m Meta) Refused(reason string) Note {
	return Note{
		ContractVersion:        Version,
		Generator:              m.Generator,
		SHA:                    m.SHA,
		Tree:                   m.Tree,
		Fidelity:               m.Fidelity,
		ReachabilityComputable: false,
		NotComputableReason:    reason,
		Rows:                   nil,
	}
}

// Write marshals a note to <dir>/<name>.json with a trailing newline.
func Write(dir, name string, n Note) error {
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644)
}
