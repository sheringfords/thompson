package harness

import (
	"math"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

var fixtureBase = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

func fxEvent(job, tx string, version uint64, status outcome.JobStatus, costs []float64, verifiedAt time.Time) outcome.OutcomeEvent {
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: "dec-" + job, JobID: job, StrategyID: tx,
		Version: version, Supersedes: version - 1, Status: status,
		OccurredAt: verifiedAt.UTC().Format(time.RFC3339Nano),
		VerifiedAt: verifiedAt.UTC().Format(time.RFC3339Nano),
	}
	cost := 0.0
	if len(costs) > 0 {
		cost = costs[0]
	}
	verified := outcome.VerifiedFailure
	if status == outcome.StatusAccepted {
		verified = outcome.VerifiedSuccess
	}
	if status == outcome.StatusUnknown {
		verified = outcome.VerifiedUnknown
	}
	ev.Attempts = []outcome.Attempt{{
		AttemptID: job + "-a", Seq: 0, ExecutorID: "arm", ArmID: "arm",
		Transport: outcome.TransportOK, LatencyMs: 100, CostUSD: &cost,
		Validation: outcome.ValidationPass, Verified: verified,
	}}
	if status == outcome.StatusAccepted || status == outcome.StatusRejected {
		ev.DecidingAttemptID = job + "-a"
	}
	return ev
}

func fxAssign(job, tx string, at time.Time) Assignment {
	return Assignment{JobID: job, Strata: "s", Treatment: tx, Probability: 1.0 / 3,
		AssignedAt: at.UTC().Format(time.RFC3339Nano), AssignerSeed: 1, AssignmentSeed: assignmentDomain}
}

func testCfg() ReportConfig {
	return ReportConfig{
		Maturation: 24 * time.Hour, Now: fixtureBase.Add(72 * time.Hour),
		MinJobs: 1, CensorGate: 0.5, QualityFloor: 0.5, MinEffect: 0.15,
		BootstrapN: 200, BootstrapSeed: 11,
	}
}

// Hand-computed fixture: t0 has 2×ACCEPTED(1,3) + REJECTED(2) + UNKNOWN(5).
// Primary = (1+3)/2 = 2.0; censored = 1/4.
func TestPrimaryAndCensoringHandComputed(t *testing.T) {
	at := fixtureBase
	as := []Assignment{fxAssign("j1", "t0", at), fxAssign("j2", "t0", at), fxAssign("j3", "t0", at), fxAssign("j4", "t0", at)}
	evs := []outcome.OutcomeEvent{
		fxEvent("j1", "t0", 1, outcome.StatusAccepted, []float64{1.0}, at),
		fxEvent("j2", "t0", 1, outcome.StatusAccepted, []float64{3.0}, at),
		fxEvent("j3", "t0", 1, outcome.StatusRejected, []float64{2.0}, at),
		fxEvent("j4", "t0", 1, outcome.StatusUnknown, []float64{5.0}, at),
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	rep := Analyze(recs, testCfg(), "t0", "t2", nil, true, true)
	st := rep.Treatments["t0"]
	if st.Matured != 4 || st.Accepted != 2 || st.Rejected != 1 || st.Unknown != 1 {
		t.Fatalf("counts wrong: %+v", st)
	}
	if st.Primary != 3.0 {
		t.Fatalf("primary=%v want 3.0 = (1+3 accepted + 2 rejected)/2 accepted; unknown spend censored", st.Primary)
	}
	if math.Abs(st.CensoredFraction-0.25) > 1e-12 {
		t.Fatalf("censored=%v want 0.25", st.CensoredFraction)
	}
	if math.Abs(st.AcceptRate-2.0/3.0) > 1e-12 {
		t.Fatalf("acceptRate=%v want 2/3", st.AcceptRate)
	}
}

// Conclusive fixture: t2 primary 1.0 vs t0 primary 4.0, identical values so
// every bootstrap resample gives diff -3.0, rel 0.75.
func TestConclusiveFixture(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	for i := 0; i < 3; i++ {
		j2, j0 := "t2j"+itoa(i), "t0j"+itoa(i)
		as = append(as, fxAssign(j2, "t2", at), fxAssign(j0, "t0", at))
		evs = append(evs,
			fxEvent(j2, "t2", 1, outcome.StatusAccepted, []float64{1.0}, at),
			fxEvent(j0, "t0", 1, outcome.StatusAccepted, []float64{4.0}, at))
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg()
	cfg.MinJobs = 3
	cfg.CensorGate = 0.05
	rep := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	if len(rep.Comparisons) != 1 {
		t.Fatalf("comparisons=%d", len(rep.Comparisons))
	}
	c := rep.Comparisons[0]
	if c.Diff != -3.0 || c.RelImprovement != 0.75 {
		t.Fatalf("diff=%v rel=%v", c.Diff, c.RelImprovement)
	}
	if c.CILow != -3.0 || c.CIHigh != -3.0 || !c.Wins || !c.MeetsBar {
		t.Fatalf("CI wrong: %+v", c)
	}
	if rep.Verdict != "CONCLUSIVE_T2_WINS" {
		t.Fatalf("verdict=%s reasons=%v", rep.Verdict, rep.Reasons)
	}
	// Determinism: identical CI across runs.
	rep2 := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	if rep2.Comparisons[0] != c {
		t.Fatal("bootstrap not deterministic")
	}
}

// Censoring gate trips: t2 50% unknown at gate 0.05 → NOT_RANKABLE.
func TestCensoringGateTrips(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	for i := 0; i < 4; i++ {
		j := "j" + itoa(i)
		as = append(as, fxAssign(j, "t2", at))
		st := outcome.StatusAccepted
		if i >= 2 {
			st = outcome.StatusUnknown
		}
		evs = append(evs, fxEvent(j, "t2", 1, st, []float64{1.0}, at))
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg()
	cfg.CensorGate = 0.05
	rep := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	if rep.Verdict != "NOT_RANKABLE" {
		t.Fatalf("verdict=%s want NOT_RANKABLE", rep.Verdict)
	}
}

// Missing costs: nil-cost job excluded from primary, counted in share.
func TestMissingCostExcluded(t *testing.T) {
	at := fixtureBase
	as := []Assignment{fxAssign("j1", "t0", at), fxAssign("j2", "t0", at), fxAssign("j3", "t2", at)}
	e1 := fxEvent("j1", "t0", 1, outcome.StatusAccepted, []float64{2.0}, at)
	e2 := fxEvent("j2", "t0", 1, outcome.StatusAccepted, []float64{9.0}, at)
	e2.Attempts[0].CostUSD = nil
	e3 := fxEvent("j3", "t2", 1, outcome.StatusAccepted, []float64{1.0}, at)
	recs := MatureJobs(as, []outcome.OutcomeEvent{e1, e2, e3}, at.Add(24*time.Hour), 24*time.Hour)
	rep := Analyze(recs, testCfg(), "t0", "t2", nil, true, true)
	st := rep.Treatments["t0"]
	if st.Primary != 2.0 {
		t.Fatalf("primary=%v want 2.0 (nil-cost excluded)", st.Primary)
	}
	if st.MeteredShare != 0.5 || st.UnmeteredJobs != 1 {
		t.Fatalf("share wrong: %+v", st)
	}
	lo, hi := rep.Sensitivity.MissingCostLow, rep.Sensitivity.MissingCostHigh
	if math.IsNaN(lo) || math.IsNaN(hi) {
		t.Fatalf("missing-cost bounds NaN: %v %v", lo, hi)
	}
}

// Late correction: version verified after cutoff sets CorrectedAfter and the
// pre-cutoff version decides.
func TestLateCorrectionMarked(t *testing.T) {
	at := fixtureBase
	as := []Assignment{fxAssign("j1", "t0", at)}
	v1 := fxEvent("j1", "t0", 1, outcome.StatusAccepted, []float64{1.0}, at)
	v2 := fxEvent("j1", "t0", 2, outcome.StatusRejected, []float64{1.0}, at.Add(48*time.Hour))
	recs := MatureJobs(as, []outcome.OutcomeEvent{v1, v2}, at.Add(24*time.Hour), 24*time.Hour)
	if len(recs) != 1 || !recs[0].Accepted || !recs[0].CorrectedAfter {
		t.Fatalf("late correction wrong: %+v", recs)
	}
}

// Immature jobs excluded: assigned 1h ago with 24h maturation means the
// cutoff (now - M) precedes assignment.
func TestImmatureExcluded(t *testing.T) {
	at := fixtureBase
	as := []Assignment{fxAssign("j1", "t0", at)}
	evs := []outcome.OutcomeEvent{fxEvent("j1", "t0", 1, outcome.StatusAccepted, []float64{1.0}, at)}
	recs := MatureJobs(as, evs, at.Add(time.Hour), 24*time.Hour)
	if len(recs) != 1 || !recs[0].HasOutcome || recs[0].Matured {
		t.Fatalf("immature handling wrong: %+v", recs)
	}
	rep := Analyze(recs, testCfg(), "t0", "t2", nil, true, true)
	if _, ok := rep.Treatments["t0"]; ok && rep.Treatments["t0"].Matured != 0 {
		t.Fatalf("immature job counted: %+v", rep.Treatments["t0"])
	}
}

// Worst-case censoring flips a fragile win to INCONCLUSIVE.
func TestWorstCaseCensoringFlips(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	// t2: 1 accepted @1.0 + 3 unresolved; t0: 4 accepted @2.0.
	as = append(as, fxAssign("w0", "t2", at))
	evs = append(evs, fxEvent("w0", "t2", 1, outcome.StatusAccepted, []float64{1.0}, at))
	for i := 1; i <= 3; i++ {
		as = append(as, fxAssign("w"+itoa(i), "t2", at))
	}
	for i := 0; i < 4; i++ {
		j := "z" + itoa(i)
		as = append(as, fxAssign(j, "t0", at))
		evs = append(evs, fxEvent(j, "t0", 1, outcome.StatusAccepted, []float64{2.0}, at))
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg()
	cfg.CensorGate = 1.0 // let the gate pass so sensitivity decides
	rep := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	if rep.Sensitivity.WorstCaseCensoringWins {
		t.Fatal("fragile win survived worst-case censoring (should not)")
	}
	if rep.Verdict != "INCONCLUSIVE" {
		t.Fatalf("verdict=%s want INCONCLUSIVE", rep.Verdict)
	}
}
