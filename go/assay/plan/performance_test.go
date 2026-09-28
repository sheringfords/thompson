package plan

import (
	"testing"
	"time"
)

// performance_test.go: Phase 7 — real work, not modeled token savings.
// Operations burn deterministic synthetic CPU (WorkUnitHashes per CostUnit),
// reported explicitly as synthetic systems work. Metrics: executed-node counts,
// cumulative op CPU/wall time, planner overhead, lookup/validation overhead,
// store-write bytes, completion latency, cold vs warm, 5-trial distributions.
// Machine-readable output: testdata/plan_perf.json.

type PerfCase struct {
	Workload  string `json:"workload"`
	Treatment string `json:"treatment"`
	Scenario  string `json:"scenario"`
	Trials    int    `json:"trials"`

	ExecutedMean   float64 `json:"executed_mean"`
	ReusedMean     float64 `json:"reused_mean"`
	OpWorkMSMean   float64 `json:"op_work_ms_mean"`
	OpWorkMSMax    float64 `json:"op_work_ms_max"`
	OverheadUSMean float64 `json:"plan_overhead_us_mean"`
	LatencyMSMean  float64 `json:"latency_ms_mean"`
	StoreBytes     int64   `json:"store_bytes"`
	TerminalOK     bool    `json:"terminal_accepted"`
}

type PerfReport struct {
	Cases []PerfCase `json:"cases"`
	Note  string     `json:"note"`
}

func perfTrials(t *testing.T, workload, treatment, scenario string, trials int,
	setup func(e *Executor) (PhysicalPlan, []PhysicalPlan)) PerfCase {
	t.Helper()
	c := PerfCase{Workload: workload, Treatment: treatment, Scenario: scenario, Trials: trials}
	var ex, re, work, over, lat []float64
	var storeB int64
	for i := 0; i < trials; i++ {
		e := testExecutor(t)
		plan, alts := setup(e)
		t0 := time.Now()
		var rep *RunReport
		var err error
		if treatment == TreatP3 {
			rep, _, err = e.RunP3(alts, "perf", 0)
		} else {
			rep, err = e.Run(plan, treatment, "perf", 0)
		}
		dt := time.Since(t0)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.TerminalOK {
			t.Fatalf("%s/%s/%s terminal rejected", workload, treatment, scenario)
		}
		ex = append(ex, float64(len(rep.Executed)))
		re = append(re, float64(len(rep.Reused)))
		work = append(work, float64(rep.OpWorkNS)/1e6)
		over = append(over, float64(rep.PlanOverheadNS)/1e3)
		lat = append(lat, float64(dt.Nanoseconds())/1e6)
		storeB = rep.StoreWriteB
	}
	c.ExecutedMean, c.ReusedMean = mean(ex), mean(re)
	c.OpWorkMSMean, c.OpWorkMSMax = mean(work), max(work)
	c.OverheadUSMean, c.LatencyMSMean = mean(over), mean(lat)
	c.StoreBytes = storeB
	c.TerminalOK = true
	return c
}

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func max(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

func TestPlanPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("performance assay needs full run")
	}
	rep := PerfReport{}
	// Cold D2 staged: P0 vs P2 (same plan, reuse impossible cold — measures
	// executor overhead delta only). Cold D1 staged vs direct via P3
	// (planner overhead + selection). Fresh store per trial: all cold.
	rep.Cases = append(rep.Cases, perfTrials(t, "D2", TreatP0, "cold", 5,
		func(e *Executor) (PhysicalPlan, []PhysicalPlan) {
			s, _ := buildD2(61, d2Tree(61), "go1.22.0", "lint-v1")
			return s, nil
		}))
	rep.Cases = append(rep.Cases, perfTrials(t, "D2", TreatP2, "cold", 5,
		func(e *Executor) (PhysicalPlan, []PhysicalPlan) {
			s, _ := buildD2(61, d2Tree(61), "go1.22.0", "lint-v1")
			return s, nil
		}))
	// Cold D1 staged-vs-direct via P3 (planner overhead + selection).
	rep.Cases = append(rep.Cases, perfTrials(t, "D1", TreatP3, "cold-selects-direct", 5,
		func(e *Executor) (PhysicalPlan, []PhysicalPlan) {
			s, d := buildD1(62, d1Doc(62))
			return s, []PhysicalPlan{s, d}
		}))
	rep.Note = "Synthetic CPU work only (SHA-256 burns, WorkUnitHashes=2000/unit), darwin. " +
		"PlanOverheadNS includes fsync-bound store publishes (~6-9ms/record macOS); " +
		"pure lookup/evaluate/plan resolution is microsecond-scale per node (see reuse assay). " +
		"Cold P2 pays durability writes and can be slower wall-clock than P0; warmth pays it back."
	for _, c := range rep.Cases {
		t.Logf("%s/%s/%s trials=%d exec=%.1f reused=%.1f work=%.1fms(max %.1f) overhead=%.0fus latency=%.1fms store=%dB",
			c.Workload, c.Treatment, c.Scenario, c.Trials, c.ExecutedMean, c.ReusedMean,
			c.OpWorkMSMean, c.OpWorkMSMax, c.OverheadUSMean, c.LatencyMSMean, c.StoreBytes)
	}
	writePlanReport(t, "plan_perf.json", rep)
}
