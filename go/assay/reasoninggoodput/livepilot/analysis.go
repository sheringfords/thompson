// Analysis: cumulative-union premises and L1/L2/L3 counterfactual costing.
//
// Premise model (frozen): each model invocation (stream step) sees all
// conversation history, so slice N's premises = union of all witnessed
// reads in steps 1..N. This is conservative (never claims less than the
// harness demonstrably showed) and never uses hidden reasoning. A slice is
// narrow iff earlier steps hadn't yet read what later steps did — early
// slices are naturally selective; late slices are broad. SARF measures how
// much expensive work sits in narrow slices.
package livepilot

import (
	"sort"
)

// CallSlice is one model invocation with mechanical premises.
type CallSlice struct {
	Index    int
	Premises map[string]string // resource -> witness digest/oid
	CostNS   int64             // wall time from part timestamps (fallback units)
	HasTools bool
}

// Live slices are built by BuildSlices (live.go), which groups stream parts
// by messageID, resolves file premises by content-derived blob OIDs, and
// prices turns by step token counts. The CallSlice/SARF/Counterfactual types
// below consume those slices.

// SARFResult holds the selectivity measurement.
type SARFResult struct {
	SARF          float64
	OpaqueFrac    float64
	MedianPremise int
	TotalReads    int
	UnknownFrac   float64
	ByCall        []float64 // per-call premise count (audit)
}

// SARF computes the selectively-addressable reasoning fraction: cost of
// slices whose premise set is STRICTLY smaller than the run's full context,
// over total slice cost. Cost = per-call wall ns (metadata fallback pending
// dev-run inspection; source recorded per run).
func SARF(calls []CallSlice, fullContext map[string]string) SARFResult {
	var total, narrow, opaqueCost, totalCost int64
	var counts []float64
	unk := 0
	uniq := map[string]bool{}
	for _, c := range calls {
		for k := range c.Premises {
			uniq[k] = true
		}
	}
	for _, c := range calls {
		counts = append(counts, float64(len(c.Premises)))
		w := c.CostNS
		if w <= 0 {
			w = 1
		}
		totalCost += w
		total++
		strict := false
		for k := range fullContext {
			if _, ok := c.Premises[k]; !ok {
				strict = true
				break
			}
		}
		if strict && len(c.Premises) > 0 {
			narrow += w
		}
		// Opaque = premises cover the full run context (nothing narrowed).
		if len(c.Premises) > 0 && len(c.Premises) >= len(fullContext) {
			opaque := true
			for k := range fullContext {
				if _, ok := c.Premises[k]; !ok {
					opaque = false
					break
				}
			}
			if opaque {
				opaqueCost += w
			}
		}
		for _, v := range c.Premises {
			if v == "UNKNOWN" {
				unk++
				break
			}
		}
	}
	r := SARFResult{ByCall: counts, TotalReads: len(uniq)}
	if totalCost > 0 {
		r.SARF = float64(narrow) / float64(totalCost)
		r.OpaqueFrac = float64(opaqueCost) / float64(totalCost)
	}
	if total > 0 {
		r.UnknownFrac = float64(unk) / float64(total)
	}
	sort.Float64s(counts)
	if len(counts) > 0 {
		r.MedianPremise = int(counts[len(counts)/2])
	}
	return r
}

// Counterfactual costs L1/L2/L3 for one contended run given the stale set
// (premise resources whose witnesses moved). All units are the run's own
// cost metric (no invented dollars). With no stale premises all four are
// zero (nothing to discard, nothing to preserve-versus-discard).
func Counterfactual(calls []CallSlice, stale map[string]bool) (l1redo, l2discard, l3discard, l3preserved int64) {
	anyStale := false
	for _, c := range calls {
		for k := range c.Premises {
			if stale[k] {
				anyStale = true
				break
			}
		}
		if anyStale {
			break
		}
	}
	if !anyStale {
		return 0, 0, 0, 0
	}
	for _, c := range calls {
		w := c.CostNS
		if w <= 0 {
			w = 1
		}
		l1redo += w
		l2discard += w
		staleSlice := false
		for k := range c.Premises {
			if stale[k] {
				staleSlice = true
				break
			}
		}
		if staleSlice {
			l3discard += w
		} else {
			l3preserved += w
		}
	}
	return l1redo, l2discard, l3discard, l3preserved
}
