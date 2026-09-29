package plan

import (
	"fmt"
	"sort"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// planner.go: P3 deterministic cost-based physical-plan selection.
//
// P3's REAL capability beyond P2: choosing among predeclared EQUIVALENT
// physical plans (staged vs direct) using currently-VALID artifacts and known
// costs. P2 always runs the fixed (staged) plan; P3 may run the direct plan
// when cold-start economics favor fewer stages, or the staged plan when
// cached intermediates make it cheaper. Correctness dominates cost: plans with
// UNKNOWN/unresolvable nodes are illegal, never merely expensive.

// VerifyPriceUnits is the flat verification price per executed node
// (documented constant; verification accompanies execution).
const VerifyPriceUnits = 1

// MissingCost marks CostUnits unknown: plans containing such nodes are illegal
// (adversarial scenario: stale/missing estimates must refuse, not guess).
const MissingCost = -1

// PlanQuote prices one physical plan under the current store state.
type PlanQuote struct {
	PlanID     string
	Legal      bool
	TotalUnits int
	NodePrices map[string]int    // node -> 0 (reuse) or CostUnits+verify
	NodeKeys   map[string]string // node -> derived execution-key digest ("": uncomputable)
	Reason     string
}

// Quote estimates plan cost WITHOUT executing: topo walk, deriving each node's
// current key from already-priced upstreams. An upstream that must execute has
// an unknowable fresh digest → downstream keys uncomputable → downstream priced
// as must-execute (conservative, no future information used).
func (e *Executor) Quote(plan PhysicalPlan) PlanQuote {
	q := PlanQuote{PlanID: plan.PlanID, Legal: true,
		NodePrices: map[string]int{}, NodeKeys: map[string]string{}}
	if err := plan.Validate(); err != nil {
		q.Legal = false
		q.Reason = "invalid plan: " + err.Error()
		return q
	}
	order, err := plan.TopoOrder()
	if err != nil {
		q.Legal = false
		q.Reason = err.Error()
		return q
	}
	byID := map[string]PlanNode{}
	for _, n := range plan.Nodes {
		byID[n.NodeID] = n
	}
	upKeys := map[string]string{}
	upArts := map[string]string{} // nodeID -> priced artifact digest (VALID reuse only)
	for _, id := range order {
		n := byID[id]
		if n.CostUnits == MissingCost {
			q.Legal = false
			q.Reason = fmt.Sprintf("node %q has missing cost estimate", id)
			return q
		}
		if _, ok := e.Reg.Ops[n.Op]; !ok {
			q.Legal = false
			q.Reason = fmt.Sprintf("node %q op %q not registered", id, n.Op)
			return q
		}
		if n.VerifierContract != "" {
			if _, ok := e.Reg.Verifiers[n.VerifierContract]; !ok {
				q.Legal = false
				q.Reason = fmt.Sprintf("node %q verifier contract unregistered", id)
				return q
			}
		}
		// Upstream must-execute (absent from upArts) → key unknowable → execute.
		blocked := false
		for _, u := range n.Upstreams {
			if _, ok := upArts[u]; !ok {
				// Upstream executes fresh OR upstream reuse digest unknown:
				// check whether upstream was priced reuse (present) — absent
				// means must-execute. Either way downstream cannot hit.
				blocked = true
				break
			}
		}
		price := n.CostUnits + VerifyPriceUnits
		if !blocked {
			key, err := NodeKey(n, selectKeys(upKeys, n.Upstreams), selectKeys(upArts, n.Upstreams))
			if err != nil {
				q.Legal = false
				q.Reason = fmt.Sprintf("node %q unresolvable: %v", id, err)
				return q
			}
			upKeys[id] = key.KeyDigest()
			q.NodeKeys[id] = key.KeyDigest()
			if stored, hit := e.Store.Lookup(key.KeyDigest()); hit {
				dec := e.Store.Evaluate(key, reuse.LiveOf(key), e.lookup)
				switch dec.Validity {
				case reuse.ValidityValid:
					price = 0
					upArts[id] = stored.ArtifactDigest
				case reuse.ValidityStale, reuse.ValidityInvalid:
					// Recompute: fresh digest unknowable → downstream blocked.
					// (upArts[id] stays absent; upKeys[id] IS set.)
				default: // UNKNOWN and anything else: fail closed
					q.Legal = false
					q.Reason = fmt.Sprintf("node %q validity UNKNOWN: %s", id, dec.Reason)
					return q
				}
			}
		}
		q.NodePrices[id] = price
		q.TotalUnits += price
	}
	q.Reason = fmt.Sprintf("estimated %d units over %d nodes", q.TotalUnits, len(order))
	return q
}

// ChoosePlan deterministically selects the cheapest LEGAL plan.
// Tie-break: lowest total, then lexicographic PlanID. Records the reason.
func (e *Executor) ChoosePlan(alts []PhysicalPlan) (PhysicalPlan, PlanQuote, error) {
	if len(alts) == 0 {
		return PhysicalPlan{}, PlanQuote{}, fmt.Errorf("plan: no candidate plans")
	}
	type scored struct {
		plan PhysicalPlan
		q    PlanQuote
	}
	var legal []scored
	for _, p := range alts {
		q := e.Quote(p)
		if q.Legal {
			legal = append(legal, scored{p, q})
		}
	}
	if len(legal) == 0 {
		return PhysicalPlan{}, PlanQuote{}, fmt.Errorf("plan: no legal candidate plan")
	}
	sort.Slice(legal, func(i, j int) bool {
		if legal[i].q.TotalUnits != legal[j].q.TotalUnits {
			return legal[i].q.TotalUnits < legal[j].q.TotalUnits
		}
		return legal[i].plan.PlanID < legal[j].plan.PlanID
	})
	best := legal[0]
	best.q.Reason = fmt.Sprintf("selected %s at %d units (%s); %d legal of %d candidates",
		best.plan.PlanID, best.q.TotalUnits, best.q.Reason, len(legal), len(alts))
	return best.plan, best.q, nil
}

// RunP3 plans then executes the chosen alternative with P2 resolution.
func (e *Executor) RunP3(alts []PhysicalPlan, runID string, crashAfter int) (*RunReport, PlanQuote, error) {
	t0 := time.Now()
	chosen, quote, err := e.ChoosePlan(alts)
	planNS := time.Since(t0).Nanoseconds()
	if err != nil {
		return nil, quote, err
	}
	rep, err := e.Run(chosen, TreatP2, runID, crashAfter)
	if rep != nil {
		rep.Treatment = TreatP3
		rep.PlanOverheadNS += planNS
		rep.Reasons["planner"] = quote.Reason
	}
	return rep, quote, err
}
