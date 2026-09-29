package materialization

import (
	"encoding/json"
	"fmt"
	"sort"
)

// analyzer.go: Phase 8 offline execution-economics analyzer (research-only).
// Operates purely on recorded execution history (no execution, no future
// knowledge beyond the recorded versions). Interface: analyze(history),
// materialization_opportunity, recomputation_frontier, break_even,
// plan_counterfactuals. Deterministic, machine-readable output.

// EconRow is one machine-readable benchmark row (shared schema for the
// economics output and the offline analyzer input).
type EconRow struct {
	Mutation string `json:"mutation"`
	Mode     string `json:"mode"`
	Plan     string `json:"plan,omitempty"`
	Exec     int    `json:"executed"`
	Reused   int    `json:"reused"`
	ExecNS   int64  `json:"exec_ns"`
	VerifyNS int64  `json:"verify_ns"`
	OverNS   int64  `json:"overhead_ns"`
	TotalNS  int64  `json:"total_ns"`
	StoreB   int64  `json:"store_bytes"`
	Verdict  string `json:"verdict"`
	TermEq   bool   `json:"terminal_eq_a"`
	False    int    `json:"false_reuse"`
}

// EconReport is the machine-readable economics output.
type EconReport struct {
	Rows   []EconRow         `json:"rows"`
	Gate   map[string]string `json:"charter_gate"`
	Decomp map[string]string `json:"b_bp_c_decomposition"`
}

// History is the recorded assay past: per-version per-mode results.
type History struct {
	Versions []VersionResult `json:"versions"`
}

// Analysis is the analyzer output.
type Analysis struct {
	Opportunity     map[string]int64   `json:"materialization_opportunity_ns"`
	StaleRate       map[string]float64 `json:"stale_work_rate"`
	ClosureSizes    map[string]int     `json:"recomputation_closure_sizes"`
	BreakEven       map[string]string  `json:"break_even"`
	Counterfactuals map[string]int64   `json:"plan_counterfactuals_ns"`
	RepeatFrequency map[string]int     `json:"reuse_frequency"`
}

// analyze runs the full offline analysis over history.
func analyze(h History) Analysis {
	a := Analysis{
		Opportunity:     map[string]int64{},
		StaleRate:       map[string]float64{},
		ClosureSizes:    map[string]int{},
		BreakEven:       map[string]string{},
		Counterfactuals: map[string]int64{},
		RepeatFrequency: map[string]int{},
	}
	byMode := map[string][]VersionResult{}
	for _, v := range h.Versions {
		byMode[v.Mode] = append(byMode[v.Mode], v)
	}
	// materialization_opportunity: per mutation, full-recompute work minus
	// materialized (C) work = the prize available before running anything new.
	for _, c := range byMode["C"] {
		var full int64
		for _, av := range byMode["A"] {
			if av.Mutation == c.Mutation {
				full = av.ExecNS + av.VerifyNS
			}
		}
		a.Opportunity[c.Mutation] = full - (c.ExecNS + c.VerifyNS)
		a.ClosureSizes[c.Mutation] = c.Executed
		if full > 0 {
			a.StaleRate[c.Mutation] = float64(c.Executed) / float64(c.Executed+c.Reused)
		}
	}
	// break_even: repeats of the no-change version needed for C's cumulative
	// coordination overhead to be repaid by per-version savings vs B.
	var bNo, cNo int64
	var bOver, cOver int64
	for _, v := range h.Versions {
		if v.Mutation != "no-change" {
			continue
		}
		switch v.Mode {
		case "B":
			bNo = v.ExecNS + v.VerifyNS
			bOver = v.OverNS
		case "C":
			cNo = v.ExecNS + v.VerifyNS
			cOver = v.OverNS
		}
	}
	if bNo > cNo {
		per := (bNo - cNo)
		extra := cOver - bOver
		if extra <= 0 {
			a.BreakEven["no-change"] = "immediate (C overhead below B already)"
		} else {
			a.BreakEven["no-change"] = fmt.Sprintf("~%d repeats to repay %dns overhead at %dns/version",
				extra/per+1, extra, per)
		}
	} else {
		a.BreakEven["no-change"] = "never at current overhead (C work >= B work)"
	}
	// plan_counterfactuals: staged-vs-direct remaining-work estimates from
	// recorded C closures (what D would have paid on each mutation).
	for _, c := range byMode["C"] {
		if c.Plan == "direct" {
			a.Counterfactuals[c.Mutation] = c.ExecNS + c.VerifyNS
		} else {
			// Staged ran: direct counterfactual ≈ record-layer work (shared
			// prefix, recorded) + direct-op + attest calibration share.
			// Computed from history means, no new execution.
			a.Counterfactuals[c.Mutation] = -1 // answered by D rows directly
		}
	}
	for _, d := range byMode["D"] {
		a.Counterfactuals[d.Mutation+"/D"] = d.ExecNS + d.VerifyNS
	}
	// reuse frequency per mode.
	for mode, vs := range byMode {
		for _, v := range vs {
			a.RepeatFrequency[mode] += v.Reused
		}
	}
	return a
}

// materializationOpportunity ranks mutations by avoidable work (top first).
func materializationOpportunity(a Analysis) []string {
	type kv struct {
		k string
		v int64
	}
	var kvs []kv
	for k, v := range a.Opportunity {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	var out []string
	for _, kv := range kvs {
		out = append(out, kv.k)
	}
	return out
}

// recomputationFrontier returns closure sizes sorted (the efficient frontier
// of what must recompute per mutation under C).
func recomputationFrontier(a Analysis) map[string]int { return a.ClosureSizes }

// breakEven returns the repayment verdicts.
func breakEven(a Analysis) map[string]string { return a.BreakEven }

// planCounterfactuals returns recorded plan-alternative costs.
func planCounterfactuals(a Analysis) map[string]int64 { return a.Counterfactuals }

// historyFromEcon converts EconRows into analyzer history input.
func historyFromEcon(rows []EconRow) History {
	h := History{}
	for _, r := range rows {
		h.Versions = append(h.Versions, VersionResult{
			Mode: r.Mode, Mutation: r.Mutation, Executed: r.Exec,
			Reused: r.Reused, ExecNS: r.ExecNS, VerifyNS: r.VerifyNS,
			OverNS: r.OverNS, Verdict: r.Verdict, Plan: r.Plan,
		})
	}
	return h
}

var _ = json.Marshal
