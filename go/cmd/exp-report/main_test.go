package main

import (
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// buildFixtureExperiment runs a small synthetic experiment through real
// treatment files: T0 static-cheap, T1 static-strong (expensive), T2
// adaptive. Ground truth favors cheap, so T2 should discover it.
func buildFixtureExperiment(t *testing.T, dir string, n int, at time.Time) {
	t.Helper()
	assigner := harness.Assigner{Seed: 99, Treatments: []string{"t0", "t1", "t2"}, Weights: []float64{1, 1, 1}}
	policy := thompson.NewDefault("cheap", "strong")
	inst := map[string]*harness.TreatmentInstance{}
	closers := map[string]func(){}
	open := func(id string, learn bool) {
		t.Helper()
		var p *thompson.Policy
		if learn {
			p = policy
		}
		ti, err := harness.OpenTreatment(dir+"/"+id, id, p, learn)
		if err != nil {
			t.Fatal(err)
		}
		inst[id] = ti
		closers[id] = func() { _ = ti.Close() }
	}
	open("t0", false)
	open("t1", false)
	open("t2", true)
	defer closers["t0"]()
	defer closers["t1"]()
	defer closers["t2"]()

	strategies := map[string]harness.Strategy{
		"t0": harness.StaticStrategy{StrategyID: "t0", Arms: []string{"cheap"}, MaxRetries: 2, Verifier: "v"},
		"t1": harness.StaticStrategy{StrategyID: "t1", Arms: []string{"strong"}, MaxRetries: 2, Verifier: "v"},
		"t2": harness.ThompsonStrategy{StrategyID: "t2", Policy: policy, MaxRetries: 2, Verifier: "v"},
	}
	rng := rand.New(rand.NewPCG(5, 6))
	for i := 0; i < n; i++ {
		jobID := "job-" + itoa(i)
		a, err := assigner.Assign(jobID, "s")
		if err != nil {
			t.Fatal(err)
		}
		jt := harness.JobTruth{
			JobID: jobID, Strata: "s",
			AssignedAt: at.Add(time.Duration(i) * time.Minute),
			ArmSuccess: map[string]float64{"cheap": 0.9, "strong": 0.4},
			ArmCost:    map[string]float64{"cheap": 0.001, "strong": 0.05},
			ArmLatency: map[string]float64{"cheap": 200, "strong": 800},
		}
		if _, err := inst[a.Treatment].RunJob(rng, strategies[a.Treatment], jt, a); err != nil {
			t.Fatal(err)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func reportCfg(now time.Time) harness.ReportConfig {
	return harness.ReportConfig{
		Maturation: 24 * time.Hour, Now: now,
		MinJobs: 5, CensorGate: 0.5, QualityFloor: 0.3, MinEffect: 0.15,
		BootstrapN: 200, BootstrapSeed: 3,
	}
}

// End-to-end: real files in, verdict out. T2 learns cheap and must beat the
// expensive static T1; assignment balance is roughly even.
func TestExpReportEndToEnd(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	buildFixtureExperiment(t, dir, 90, at)

	var allAssign []harness.Assignment
	var allEvents []outcome.OutcomeEvent
	for _, n := range []string{"t0", "t1", "t2"} {
		as, evs, err := harness.LoadTreatmentDir(dir + "/" + n)
		if err != nil {
			t.Fatal(err)
		}
		allAssign = append(allAssign, as...)
		allEvents = append(allEvents, evs...)
	}
	if len(allAssign) != 90 {
		t.Fatalf("assigned=%d want 90 (every job accounted)", len(allAssign))
	}
	counts := map[string]int{}
	for _, a := range allAssign {
		counts[a.Treatment]++
	}
	for _, n := range []string{"t0", "t1", "t2"} {
		if counts[n] < 20 || counts[n] > 40 {
			t.Fatalf("treatment %s count=%d (want ~30)", n, counts[n])
		}
	}

	rep, err := buildReport(allAssign, allEvents, []string{"t0", "t1", "t2"}, "t0", "t2", []string{"t1"}, reportCfg(at.Add(72*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Comparisons) != 2 {
		t.Fatalf("comparisons=%d want t2-t0 and t2-t1", len(rep.Comparisons))
	}
	var t2t1 *harness.Comparison
	for i := range rep.Comparisons {
		if rep.Comparisons[i].Pair == "t2-t1" {
			t2t1 = &rep.Comparisons[i]
		}
	}
	if t2t1 == nil || !t2t1.Wins {
		t.Fatalf("t2 must beat expensive static t1: %+v", t2t1)
	}
	t.Logf("verdict=%s reasons=%v", rep.Verdict, rep.Reasons)
}

// Refusal: analysis before the final job matures.
func TestExpReportRefusesImmature(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	buildFixtureExperiment(t, dir, 6, at)
	var allAssign []harness.Assignment
	var allEvents []outcome.OutcomeEvent
	for _, n := range []string{"t0", "t1", "t2"} {
		as, evs, err := harness.LoadTreatmentDir(dir + "/" + n)
		if err != nil {
			t.Fatal(err)
		}
		allAssign = append(allAssign, as...)
		allEvents = append(allEvents, evs...)
	}
	_, err := buildReport(allAssign, allEvents, []string{"t0", "t1", "t2"}, "t0", "t2", []string{"t1"}, reportCfg(at.Add(time.Hour)))
	if err == nil {
		t.Fatal("immature analysis accepted (must refuse)")
	}
	var r *refused
	if !errors.As(err, &r) {
		t.Fatalf("wrong error type: %T %v", err, err)
	}
	if exitCode(err) != 2 {
		t.Fatalf("exit=%d want 2", exitCode(err))
	}
}
