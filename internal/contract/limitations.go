package contract

import "encoding/json"

// Canonical scope values: WHO cannot do the thing. The distinction is the point
// — it tells a reader whether waiting helps. A backend limit can be fixed, an
// analyzer limit moves when the pin moves, a language limit never moves.
//
// These are NOT a closed enum on the wire. Architext deliberately left scope
// and effect unconstrained after both postures were tested on real artifacts:
// `fidelity` was left an open string and magma shipped "semantic" with zero
// breakage, while `kind` was pinned as an enum and shipping "init" made a 15 MB
// artifact wholly invalid until a two-sided fix landed. An unknown value must
// cost one unrecognised annotation, never a rejected document — so nothing here
// validates, and a backend may emit a value this list does not carry.
const (
	ScopeLanguage = "language" // inherent to the analysed language; never moves
	ScopeAnalyzer = "analyzer" // the pinned analyzer's limit; moves when the pin moves
	ScopeBackend  = "backend"  // magma has not built it yet; we can fix it
)

// Canonical effect values: WHICH WAY the limitation errs. This is the field a
// consumer weights a finding by, so in practice it matters more than scope —
// "over-approximates-live" is what lets a viewer say "these counts are
// suppressed by a known limitation" instead of silently rendering zero.
//
// Expect this set to grow fastest: it enumerates failure modes, and failure
// modes multiply per backend.
const (
	EffectOverApproximatesLive = "over-approximates-live" // errs toward LIVE: findings suppressed
	EffectMayOmitEdges         = "may-omit-edges"         // real calls missing from the graph
	EffectMayOmitNodes         = "may-omit-nodes"         // real functions missing from the graph
)

// Limitation is one declared thing a backend cannot do, attributed so a reader
// can tell whether it is permanent. It is STATIC: a property of (backend,
// version), identical across every artifact that backend emits, and therefore
// cacheable and dedupable by a consumer. Per-run measurements go in Disclosure.
type Limitation struct {
	ID          string `json:"id"`          // stable slug; lets a consumer suppress one limitation without matching prose
	Scope       string `json:"scope"`       // see Scope* constants; open set
	Attribution string `json:"attribution"` // the specific limiter, with a version where relevant
	Description string `json:"description"` // one sentence, written for a human reader
	Effect      string `json:"effect"`      // see Effect* constants; open set
	// EvidencedBy optionally names the Disclosure key that QUANTIFIES this
	// limitation on this run — e.g. root_ratio for over-rooting. Architext
	// proposed the inverse (relates_to, on the count); this direction is
	// equivalent and fits a struct of named counts rather than a list.
	EvidencedBy string `json:"evidenced_by,omitempty"`
}

// Limitations is a slice with a guaranteed non-null marshalling.
type Limitations []Limitation

// MarshalJSON guarantees limitations serialize as an ARRAY, never null.
//
// `null` and `[]` are different claims — "limitations unknown" versus "none
// declared" — and only the second is ever true of a backend magma ships. This
// is enforced on the type rather than left to each backend because the
// identical invariant on Signature was implemented in the Go backend only, and
// the Rust backend did not inherit it: 68% of functions emitted "params": null
// and Architext's validator rejected the first real Rust artifact over it.
func (l Limitations) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]Limitation(l))
}

// Disclosure is what THIS run measured, as distinct from what the backend
// always cannot do. Kept a sibling of Limitations rather than merged into it
// because the two have different lifetimes and a consumer must be able to ask
// "is this a fact about the tool, or about my code?" — merging would undercut
// the exact clarity Attribution exists to create.
type Disclosure struct {
	Nodes        int `json:"nodes"`
	Roots        int `json:"roots"`
	Generated    int `json:"generated"`
	DynamicEdges int `json:"dynamic_edges"`
	// RootRatio is Roots/Nodes, rounded to three places. It is the single number
	// that would have caught the measured Bevy case, where 136 of 156 nodes were
	// roots (87%) and the map reported ZERO dead functions — rendered downstream
	// as "no dead code, no test-only code", read as a clean bill of health
	// rather than as absence of evidence.
	RootRatio float64 `json:"root_ratio"`
}
