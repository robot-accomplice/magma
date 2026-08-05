package backend

import "github.com/robot-accomplice/magma/internal/contract"

// Each backend declares its limitations next to the code they describe, so the
// two cannot drift. They are static facts about (backend, analyzer version) —
// per-run measurements are derived generically by contract.Graph.Finalize.
//
// Every limitation here was TRUE and documented only in README prose before
// v0.3.0, which meant no machine consumer could see any of them: a downstream
// gate weighting a dead-code candidate had no way to know the map might be
// suppressing findings.

// goLimitations declares what the Go backend cannot do.
func goLimitations() contract.Limitations {
	return contract.Limitations{{
		ID:          "go-closure-edges",
		Scope:       contract.ScopeBackend,
		Attribution: "magma go backend",
		Description: "calls routed through closures or synthetic wrappers are not emitted as node edges, so a function called only through a closure can appear unreachable",
		Effect:      contract.EffectMayOmitEdges,
		EvidencedBy: "dynamic_edges",
	}}
}

// rustLimitations declares what the Rust backend cannot do. Both are MEASURED,
// not estimated.
//
// The over-rooting one is why this field exists at all. rust-analyzer reports a
// derive-generated method's own visibility as public, so on a derive-heavy
// crate the roots swamp the graph: measured on a real Bevy project at 136 of
// 156 nodes rooted (87%) with ZERO dead functions reported. Every number magma
// emitted was accurate, and a consumer computing dead = !reachable && !root
// rendered it as "no dead code, no test-only code" — false reassurance, which
// is worse than a refusal.
//
// Deliberately NOT fixed rather than not-yet-noticed: narrowing roots before
// format-args and for-loop resolution cover non-concrete types would
// manufacture FALSE dead code, which is the worse error. The second limitation
// below is precisely that precondition, which is why the two ship together.
func rustLimitations() contract.Limitations {
	return contract.Limitations{
		{
			ID:          "rust-derive-over-rooting",
			Scope:       contract.ScopeAnalyzer,
			Attribution: "rust-analyzer (ra_ap_* 0.0.343)",
			Description: "derive-generated methods report their own visibility as public, so derive-heavy crates over-root and report few or no dead functions",
			Effect:      contract.EffectOverApproximatesLive,
			EvidencedBy: "root_ratio",
		},
		{
			ID:          "rust-format-args-concrete-only",
			Scope:       contract.ScopeBackend,
			Attribution: "magma rust backend",
			Description: "format-args and for-loop desugaring resolve only concrete Adt types; .to_string(), dyn Trait and generic receivers are not resolved to an impl",
			Effect:      contract.EffectMayOmitEdges,
		},
	}
}
