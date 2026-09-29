package verifyresolution

import (
	"encoding/json"
	"sort"
)

// offline.go: Phase 12 offline historical resolution analysis (P1).
// Answers from recorded history: how many production runs conservative
// coupling would have repeated unnecessarily, and how verification-only
// churn separates from computation churn. Deterministic, machine-readable.
// Standalone types (no test coupling — cf. the EconRow lesson).

// ResRecord is one recorded resolution (production code writes these;
// tests synthesize them).
type ResRecord struct {
	Artifact   string `json:"artifact"`
	Resolution string `json:"resolution"` // REUSE | VERIFY | RECOMPUTE | UNKNOWN
	Contract   string `json:"contract"`
	ProdNS     int64  `json:"prod_ns"`
	VerNS      int64  `json:"verify_ns"`
}

// OfflineAnalysis is the deterministic counterfactual output.
type OfflineAnalysis struct {
	VerifyOpportunities int            `json:"verify_opportunities"`
	RecomputeNeeded     int            `json:"recompute_needed"`
	ReuseHits           int            `json:"reuse_hits"`
	Unknowns            int            `json:"unknowns"`
	ProdAvoidedNS       int64          `json:"production_avoided_ns"`
	VerifyChurnNS       int64          `json:"verification_churn_ns"`
	ByContract          map[string]int `json:"resolutions_by_contract"`
}

// AnalyzeResolutions separates verification-only churn (VERIFY resolutions:
// production reusable, only claims churn) from computation churn
// (RECOMPUTE). Conservative coupling would have reproduced every VERIFY row.
func AnalyzeResolutions(rows []ResRecord) OfflineAnalysis {
	a := OfflineAnalysis{ByContract: map[string]int{}}
	for _, r := range rows {
		a.ByContract[r.Contract]++
		switch r.Resolution {
		case "VERIFY":
			a.VerifyOpportunities++
			a.ProdAvoidedNS += r.ProdNS
			a.VerifyChurnNS += r.VerNS
		case "RECOMPUTE":
			a.RecomputeNeeded++
		case "REUSE":
			a.ReuseHits++
		default:
			a.Unknowns++
		}
	}
	return a
}

// RankContracts orders contracts by VERIFY opportunity (where re-verification
// capacity would pay most).
func RankContracts(a OfflineAnalysis, rows []ResRecord) []string {
	byC := map[string]int{}
	for _, r := range rows {
		if r.Resolution == "VERIFY" {
			byC[r.Contract]++
		}
	}
	type kv struct {
		k string
		v int
	}
	var kvs []kv
	for k, v := range byC {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	var out []string
	for _, kv := range kvs {
		out = append(out, kv.k)
	}
	return out
}

var _ = json.Marshal
