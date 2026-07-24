// Package backend holds the per-language call-graph extractors. Each backend
// leans on a library that does the parsing/analysis heavy lifting (Go: the
// x/tools packages+ssa+rta stack; other languages, one at a time: a tree-sitter
// grammar) and builds only the graph walk on top. A language whose extractor is
// not yet built, or whose graph is not computable, is REFUSED with an honest
// reason — never faked.
package backend

import (
	"github.com/robot-accomplice/magma/internal/contract"
	"github.com/robot-accomplice/magma/internal/detect"
)

// Progress receives a short, human-readable stage label as analysis proceeds. It
// may be nil (no reporting). It exists so the CLI can show a live status line
// without the backend knowing anything about terminals.
type Progress func(stage string)

// Backend extracts the call graph for one language.
type Backend interface {
	// Language is the token this backend serves (matches detect.Lang).
	Language() string

	// BuildGraph runs the real analysis over repo and returns the call graph,
	// or a refused graph (Computable=false) with a reason. meta carries the
	// provenance (sha, dirty, generator) the backend must not compute itself.
	// progress (may be nil) is called with a stage label at each phase.
	BuildGraph(repo string, meta contract.Meta, progress Progress) (contract.Graph, error)
}

// registry maps a language to its backend, populated by each backend's init.
var registry = map[detect.Lang]Backend{}

func register(l detect.Lang, b Backend) { registry[l] = b }

// For returns the backend for a language, or (nil, false) when none is built.
func For(l detect.Lang) (Backend, bool) {
	b, ok := registry[l]
	return b, ok
}
