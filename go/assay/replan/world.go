package replan

import (
	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
)

// world.go: deterministic assay world state. Alternatives are rebuilt from
// the CURRENT world on every assessment, so triggers act by mutating the
// world, never by hand-editing plans.

// WorldState carries every world input the assay plans depend on.
type WorldState struct {
	Seed         uint64
	Workload     string            // "D1" | "D2"
	Doc          string            // digest
	Tree         string            // digest
	Toolchain    string            // label
	LintCfg      string            // label
	CostOverride map[string]int    // nodeID -> CostUnits (plan.MissingCost allowed)
	OpVersion    map[string]string // nodeID -> replacement op version
	VerifierOf   map[string]string // nodeID -> replacement contract digest
	Extra        map[string]string // scenario inputs (e.g. wide-fan root)
}

// BaseWorldState builds the unmutated world for seed.
func BaseWorldState(workload string, seed uint64) WorldState {
	w := WorldState{Seed: seed, Workload: workload,
		CostOverride: map[string]int{}, OpVersion: map[string]string{},
		VerifierOf: map[string]string{}, Extra: map[string]string{}}
	if workload == "D1" {
		w.Doc = plan.D1Doc(seed)
	} else {
		w.Tree = plan.D2Tree(seed)
		w.Toolchain = "go1.22.0"
		w.LintCfg = "lint-v1"
	}
	return w
}

// Plans rebuilds staged/direct alternatives from the current world,
// applying cost/version/verifier overrides by node id.
func (w WorldState) Plans() (staged, direct plan.PhysicalPlan) {
	if w.Workload == "D1" {
		staged, direct = plan.BuildD1(w.Seed, w.Doc)
	} else {
		staged, direct = plan.BuildD2(w.Seed, w.Tree, w.Toolchain, w.LintCfg)
	}
	applyOverrides(&staged, w)
	applyOverrides(&direct, w)
	return staged, direct
}

func applyOverrides(p *plan.PhysicalPlan, w WorldState) {
	for i, n := range p.Nodes {
		if c, ok := w.CostOverride[n.NodeID]; ok {
			p.Nodes[i].CostUnits = c
		}
		if v, ok := w.OpVersion[n.NodeID]; ok {
			p.Nodes[i].OpVersion = v
		}
		if vc, ok := w.VerifierOf[n.NodeID]; ok {
			p.Nodes[i].VerifierContract = vc
		}
	}
}
