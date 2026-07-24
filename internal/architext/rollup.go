package architext

import (
	"sort"

	"github.com/robot-accomplice/magma/internal/contract"
)

// Module is one package in the coarse tier: its member function ids and rollup metrics.
type Module struct {
	ID          string       `json:"id"`
	Pkg         string       `json:"pkg"`
	FunctionIDs []string     `json:"function_ids"`
	Counts      ModuleCounts `json:"counts"`
	FanIn       int          `json:"fan_in"`
	FanOut      int          `json:"fan_out"`
}

// ModuleCounts summarizes a module's function population.
type ModuleCounts struct {
	Functions int `json:"functions"`
	Dead      int `json:"dead"`
	TestOnly  int `json:"test_only"`
}

// ModuleCall is one aggregated inter-module edge in the coarse tier.
type ModuleCall struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Count      int    `json:"count"`
	HasDynamic bool   `json:"has_dynamic"`
}

func rollup(nodes []contract.Node, edges []contract.Edge, ids map[int]string) ([]Module, []ModuleCall) {
	pkgOf := make(map[int]string, len(nodes)) // node.ID -> package
	mods := map[string]*Module{}
	for _, n := range nodes {
		pkgOf[n.ID] = n.Pkg
		mid := moduleID(n.Pkg)
		m, ok := mods[mid]
		if !ok {
			m = &Module{ID: mid, Pkg: n.Pkg}
			mods[mid] = m
		}
		m.FunctionIDs = append(m.FunctionIDs, ids[n.ID])
		m.Counts.Functions++
		if n.IsDead() {
			m.Counts.Dead++
		}
		if n.IsTestOnly() {
			m.Counts.TestOnly++
		}
	}

	type mkey struct{ from, to string }
	agg := map[mkey]*ModuleCall{}
	for _, e := range edges {
		from, to := moduleID(pkgOf[e.From]), moduleID(pkgOf[e.To])
		if from == to {
			continue // intra-module calls are not inter-module edges
		}
		k := mkey{from, to}
		mc, ok := agg[k]
		if !ok {
			mc = &ModuleCall{From: from, To: to}
			agg[k] = mc
		}
		mc.Count++
		if e.Kind == "dynamic" {
			mc.HasDynamic = true
		}
	}

	// Module fan is the module-graph degree: the number of DISTINCT inter-module
	// edges in/out of each module, NOT the summed underlying call counts. Counting
	// one per aggregated module_call (after dedup) is what makes it "distinct" — a
	// module pair joined by many underlying calls still contributes exactly 1.
	for _, mc := range agg {
		mods[mc.From].FanOut++
		mods[mc.To].FanIn++
	}

	outMods := make([]Module, 0, len(mods))
	for _, m := range mods {
		sort.Strings(m.FunctionIDs)
		outMods = append(outMods, *m)
	}
	outCalls := make([]ModuleCall, 0, len(agg))
	for _, mc := range agg {
		outCalls = append(outCalls, *mc)
	}
	return outMods, outCalls
}
