package architext

import "github.com/robot-accomplice/magma/internal/contract"

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
	return nil, nil // replaced in Task 6
}
