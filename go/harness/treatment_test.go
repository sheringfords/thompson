package harness

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func testAssigner() Assigner {
	return Assigner{Seed: 42, Treatments: []string{"t0", "t1", "t2"}, Weights: []float64{1, 1, 1}}
}

// Assignment is pure: same inputs reproduce across instances (restarts).
func TestAssignDeterministicAcrossInstances(t *testing.T) {
	a1 := testAssigner()
	a2 := testAssigner()
	for _, job := range []string{"job-1", "job-2", "job-odd", "job-1000"} {
		x, err := a1.Assign(job, "s1")
		if err != nil {
			t.Fatal(err)
		}
		y, err := a2.Assign(job, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if x != y {
			t.Fatalf("restart changed assignment for %s: %+v vs %+v", job, x, y)
		}
		if x.Probability != 1.0/3.0 {
			t.Fatalf("prob=%v", x.Probability)
		}
	}
}

// Weights calibrate: 1:1:2 over 9000 jobs lands near thirds.
func TestAssignWeightCalibration(t *testing.T) {
	a := Assigner{Seed: 7, Treatments: []string{"t0", "t1", "t2"}, Weights: []float64{1, 1, 2}}
	counts := map[string]int{}
	for i := 0; i < 9000; i++ {
		as, err := a.Assign("job-"+string(rune('a'+i%26))+itoa(i), "s")
		if err != nil {
			t.Fatal(err)
		}
		counts[as.Treatment]++
	}
	if counts["t0"] < 1800 || counts["t0"] > 2700 {
		t.Fatalf("t0=%d want ~2250", counts["t0"])
	}
	if counts["t2"] < 4000 || counts["t2"] > 5000 {
		t.Fatalf("t2=%d want ~4500", counts["t2"])
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

func testTruth(id string, at time.Time) JobTruth {
	return JobTruth{
		JobID: id, Strata: "s", AssignedAt: at,
		ArmSuccess: map[string]float64{"cheap": 0.9, "strong": 0.5},
		ArmCost:    map[string]float64{"cheap": 0.001, "strong": 0.02},
		ArmLatency: map[string]float64{"cheap": 200, "strong": 800},
	}
}

// RunJob persists assignment before execution and settles exactly once;
// T2 learns, static treatments ledger without learning.
func TestRunJobIsolationAndLearning(t *testing.T) {
	dir := t.TempDir()
	a := testAssigner()
	policy := thompson.NewDefault("cheap", "strong")
	t2, err := OpenTreatment(dir+"/t2", "t2", policy, true)
	if err != nil {
		t.Fatal(err)
	}
	defer t2.Close()
	t0, err := OpenTreatment(dir+"/t0", "t0", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer t0.Close()
	if t2.Store == t0.Store || t2.Dir == t0.Dir || t2.Policy == nil {
		t.Fatal("treatment state not isolated")
	}

	rng := rand.New(rand.NewPCG(1, 2))
	static := StaticStrategy{StrategyID: "t0", Arms: []string{"cheap"}, MaxRetries: 0, Verifier: "v"}
	adapt := ThompsonStrategy{StrategyID: "t2", Policy: policy, MaxRetries: 0, Verifier: "v"}
	at := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

	asg, _ := a.Assign("job-1", "s")
	ev, err := t0.RunJob(rng, static, testTruth("job-1", at), asg)
	if err != nil {
		t.Fatal(err)
	}
	if ev.StrategyID != "t0" || ev.Version != 1 {
		t.Fatalf("bad settled event: %+v", ev)
	}
	if policy.TotalPulls() != 0 {
		t.Fatal("static treatment taught the shared policy (must be isolated)")
	}

	asg2, _ := a.Assign("job-2", "s")
	if _, err := t2.RunJob(rng, adapt, testTruth("job-2", at), asg2); err != nil {
		t.Fatal(err)
	}
	if policy.TotalPulls() != 1 {
		t.Fatalf("adaptive pulls=%d want 1", policy.TotalPulls())
	}
	// The settled event is durable in t0's ledger.
	latest, ok := t0.Store.Latest("job-1")
	if !ok || latest.Status != outcome.StatusAccepted {
		t.Fatalf("t0 ledger wrong: %+v", latest)
	}
}

// Samples without historical data: hand-computed normal-equation check.
// σ=1, δ=0.5, α=0.05, power=0.8 → 2(1.96+0.84)²/0.25 = 62.72.
func TestRequiredPerGroupHandComputed(t *testing.T) {
	n := RequiredPerGroup(1.0, 2.0, 0.25, 0.05, 0.8)
	if math.Abs(n-62.72) > 0.5 {
		t.Fatalf("n=%v want ~62.72", n)
	}
	if RequiredPerGroup(0, 2.0, 0.25, 0.05, 0.8) != 0 {
		t.Fatal("zero variance must give zero")
	}
	for _, p := range []float64{0.975, 0.8, 0.5} {
		if math.Abs(normalQuantile(p)-normalRef(p)) > 1e-6 {
			t.Fatalf("quantile(%v) off", p)
		}
	}
}

func normalRef(p float64) float64 {
	switch p {
	case 0.975:
		return 1.9599639861201952
	case 0.8:
		return 0.8416212285728283
	case 0.5:
		return 0
	}
	return 0
}

func TestDecideTerminalMapping(t *testing.T) {
	mk := func(v outcome.VerifiedOutcome) outcome.Attempt {
		return outcome.Attempt{AttemptID: "x", Verified: v}
	}
	if id, st := decide([]outcome.Attempt{mk(outcome.VerifiedFailure), mk(outcome.VerifiedSuccess)}); st != outcome.StatusAccepted || id != "x" {
		t.Fatalf("accept: %v %v", id, st)
	}
	if _, st := decide([]outcome.Attempt{mk(outcome.VerifiedFailure)}); st != outcome.StatusRejected {
		t.Fatalf("reject: %v", st)
	}
	if _, st := decide(nil); st != outcome.StatusUnknown {
		t.Fatalf("empty tape must be UNKNOWN, got %v", st)
	}
}
