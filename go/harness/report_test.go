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

// Regression for commercial gate B: point relative improvement (0.57) clears
// the 0.15 bar, but the bootstrap relative CI dips to ~0.025. Under the old
// point-estimate rule this fixture read CONCLUSIVE_T2_WINS; the strict rule
// (whole CI above the bar) must report INCONCLUSIVE.
func TestStrictGateRegressionFixture(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	for i, c := range []float64{1, 1, 3.9, 3.9} {
		j := "g" + itoa(i)
		as = append(as, fxAssign(j, "t2", at))
		evs = append(evs, fxEvent(j, "t2", 1, outcome.StatusAccepted, []float64{c}, at))
	}
	for i := 0; i < 4; i++ {
		j := "h" + itoa(i)
		as = append(as, fxAssign(j, "t0", at))
		evs = append(evs, fxEvent(j, "t0", 1, outcome.StatusAccepted, []float64{4.0}, at))
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg()
	cfg.MinJobs = 4
	cfg.CensorGate = 0.05
	rep := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	if len(rep.Comparisons) != 1 {
		t.Fatalf("comparisons=%d", len(rep.Comparisons))
	}
	c := rep.Comparisons[0]
	if !c.Wins {
		t.Fatalf("fixture must win on difference: %+v", c)
	}
	if c.RelImprovement < cfg.MinEffect {
		t.Fatalf("point rel=%v must clear the bar (else fixture is wrong)", c.RelImprovement)
	}
	if c.RelCILow >= cfg.MinEffect {
		t.Fatalf("rel CI low=%v clears the bar: fixture cannot discriminate", c.RelCILow)
	}
	if c.MeetsBar {
		t.Fatal("strict gate passed on wide CI (gate B broken)")
	}
	if rep.Verdict != "INCONCLUSIVE" {
		t.Fatalf("verdict=%s reasons=%v (want INCONCLUSIVE)", rep.Verdict, rep.Reasons)
	}
}

// Observed variance sizes collection from measured jobs; the historical
// contract carries provenance and rejects bad inputs.
func TestObservedVarianceAndHistorical(t *testing.T) {
	at := fixtureBase
	as := []Assignment{fxAssign("j1", "t2", at), fxAssign("j2", "t2", at), fxAssign("j3", "t2", at), fxAssign("j4", "t2", at)}
	evs := []outcome.OutcomeEvent{
		fxEvent("j1", "t2", 1, outcome.StatusAccepted, []float64{1.0}, at),
		fxEvent("j2", "t2", 1, outcome.StatusAccepted, []float64{2.0}, at),
		fxEvent("j3", "t2", 1, outcome.StatusAccepted, []float64{3.0}, at),
		fxEvent("j4", "t2", 1, outcome.StatusAccepted, []float64{4.0}, at),
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	mean, sd, n := ObservedVariance(recs, []string{"t2"})
	if n != 4 || math.Abs(mean-2.5) > 1e-12 {
		t.Fatalf("mean=%v n=%d", mean, n)
	}
	if math.Abs(sd-math.Sqrt(5.0/3.0)) > 1e-9 {
		t.Fatalf("sd=%v want sqrt(5/3)", sd)
	}
	h := HistoricalSampleInput{Source: "assumption:charter", CollectedAt: "2026-01-01",
		BaselineMean: 2.0, Stddev: 1.0, Alpha: 0.05, Power: 0.8, MinRelEffect: 0.25}
	got, err := RequiredFromHistorical(h)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-RequiredPerGroup(1.0, 2.0, 0.25, 0.05, 0.8)) > 1e-9 {
		t.Fatal("historical wrapper diverges from formula")
	}
	bad := h
	bad.Source = ""
	if _, err := RequiredFromHistorical(bad); err == nil {
		t.Fatal("sourceless input accepted")
	}
	bad = h
	bad.Stddev = 0
	if _, err := RequiredFromHistorical(bad); err == nil {
		t.Fatal("zero-variance input accepted")
	}
}

// The report carries labeled observed-variance sizing.
func TestReportSampleSizeLabeled(t *testing.T) {
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
	rep := Analyze(recs, testCfg(), "t0", "t2", nil, true, true)
	if rep.SampleSize.Source != "synthetic-observed" {
		t.Fatalf("source=%q (must be labeled)", rep.SampleSize.Source)
	}
	if n := rep.SampleSize.Entries["t2-t0"]; n <= 0 {
		t.Fatalf("per-pair n=%v", n)
	}
}
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

// A2: zero candidate success → zero valid draws → conclusive refused.
func TestBootstrapZeroSuccessRefused(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	for i := 0; i < 5; i++ {
		j := "z" + itoa(i)
		as = append(as, fxAssign(j, "t2", at))
		evs = append(evs, fxEvent(j, "t2", 1, outcome.StatusRejected, []float64{1.0}, at))
		k := "y" + itoa(i)
		as = append(as, fxAssign(k, "t0", at))
		evs = append(evs, fxEvent(k, "t0", 1, outcome.StatusAccepted, []float64{1.0}, at))
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	rep := Analyze(recs, testCfg(), "t0", "t2", nil, true, true)
	c := rep.Comparisons[0]
	if c.ValidDraws != 0 || c.ValidFraction != 0 {
		t.Fatalf("valid=%d frac=%v want 0", c.ValidDraws, c.ValidFraction)
	}
	if c.InvalidReasons["no-candidate-success"] != 200 {
		t.Fatalf("reasons wrong: %+v", c.InvalidReasons)
	}
	if rep.Verdict == "CONCLUSIVE_T2_WINS" {
		t.Fatal("conclusive verdict on zero information")
	}
}

// A2: sparse successes on both arms → fractional coverage → refused.
func TestBootstrapSparseCoverageRefused(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	for i := 0; i < 30; i++ {
		j := "s" + itoa(i)
		as = append(as, fxAssign(j, "t2", at))
		st := outcome.StatusRejected
		if i == 0 {
			st = outcome.StatusAccepted
		}
		evs = append(evs, fxEvent(j, "t2", 1, st, []float64{1.0}, at))
		k := "b" + itoa(i)
		as = append(as, fxAssign(k, "t0", at))
		st0 := outcome.StatusRejected
		if i == 0 {
			st0 = outcome.StatusAccepted
		}
		evs = append(evs, fxEvent(k, "t0", 1, st0, []float64{1.0}, at))
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg()
	cfg.BootstrapN = 2000
	rep := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	c := rep.Comparisons[0]
	if c.ValidFraction <= 0 || c.ValidFraction >= 0.5 {
		t.Fatalf("fraction=%v want in (0, 0.5)", c.ValidFraction)
	}
	if rep.Verdict == "CONCLUSIVE_T2_WINS" {
		t.Fatal("conclusive verdict on fractional coverage")
	}
	found := false
	for _, r := range rep.Reasons {
		if strings.Contains(r, "bootstrap coverage") {
			found = true
		}
	}
	if !found {
		t.Fatalf("coverage reason missing: %v", rep.Reasons)
	}
}

// A2: fully valid ordinary case keeps fraction 1.
func TestBootstrapFullyValid(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	for i := 0; i < 3; i++ {
		j2, j0 := "v2"+itoa(i), "v0"+itoa(i)
		as = append(as, fxAssign(j2, "t2", at), fxAssign(j0, "t0", at))
		evs = append(evs,
			fxEvent(j2, "t2", 1, outcome.StatusAccepted, []float64{1.0}, at),
			fxEvent(j0, "t0", 1, outcome.StatusAccepted, []float64{4.0}, at))
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	rep := Analyze(recs, testCfg(), "t0", "t2", nil, true, true)
	c := rep.Comparisons[0]
	if c.ValidFraction != 1 || c.InvalidDraws != 0 {
		t.Fatalf("valid=%v invalid=%d", c.ValidFraction, c.InvalidDraws)
	}
}

// A3: nonuniform allocation is refused, not silently analyzed.
func TestAllocationRefusal(t *testing.T) {
	at := fixtureBase
	mk := func(job, tx string, prob float64) (Assignment, outcome.OutcomeEvent) {
		a := fxAssign(job, tx, at)
		a.Probability = prob
		return a, fxEvent(job, tx, 1, outcome.StatusAccepted, []float64{1.0}, at)
	}
	a1, e1 := mk("j1", "t2", 0.5)
	a2, e2 := mk("j2", "t0", 0.5)
	recs := MatureJobs([]Assignment{a1, a2}, []outcome.OutcomeEvent{e1, e2}, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg()
	cfg.ExpectedWeights = map[string]float64{"t2": 1.0 / 3, "t0": 1.0 / 3}
	rep := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	if rep.Verdict != "NOT_RANKABLE" {
		t.Fatalf("verdict=%s want NOT_RANKABLE", rep.Verdict)
	}
	found := false
	for _, g := range rep.Gates {
		if g.Name == "allocation" && !g.Pass {
			found = true
		}
	}
	if !found {
		t.Fatalf("allocation gate missing: %+v", rep.Gates)
	}
	// Charter-matching weights pass the gate (verdict decided elsewhere).
	a3, e3 := mk("j3", "t2", 1.0/3)
	a4, e4 := mk("j4", "t0", 1.0/3)
	recs2 := MatureJobs([]Assignment{a3, a4}, []outcome.OutcomeEvent{e3, e4}, at.Add(24*time.Hour), 24*time.Hour)
	rep2 := Analyze(recs2, cfg, "t0", "t2", nil, true, true)
	for _, g := range rep2.Gates {
		if g.Name == "allocation" && !g.Pass {
			t.Fatalf("charter-matching allocation refused: %+v", g)
		}
	}
}

// A4: high-PENDING treatment previously passed the floor and censor gates;
// unresolved mass must now trip the censor gate.
func TestHighPendingTripsCensor(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	as = append(as, fxAssign("ok", "t2", at))
	evs = append(evs, fxEvent("ok", "t2", 1, outcome.StatusAccepted, []float64{1.0}, at))
	for i := 0; i < 9; i++ {
		j := "p" + itoa(i)
		as = append(as, fxAssign(j, "t2", at))
		evs = append(evs, fxEvent(j, "t2", 1, outcome.StatusPending, []float64{1.0}, at))
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg()
	cfg.CensorGate = 0.05
	rep := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	if rep.Treatments["t2"].CensoredFraction != 0.9 {
		t.Fatalf("censored=%v want 0.9", rep.Treatments["t2"].CensoredFraction)
	}
	if rep.Verdict != "NOT_RANKABLE" {
		t.Fatalf("verdict=%s want NOT_RANKABLE", rep.Verdict)
	}
}

// A4: missing-cost gate trips on heavy unmetered share.
func TestMissingCostGateTrips(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	for i := 0; i < 4; i++ {
		j := "m" + itoa(i)
		as = append(as, fxAssign(j, "t2", at))
		ev := fxEvent(j, "t2", 1, outcome.StatusAccepted, []float64{1.0}, at)
		if i > 0 {
			ev.Attempts[0].CostUSD = nil
		}
		evs = append(evs, ev)
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg() // MaxUnmeteredShare 0 -> default 0.10; share here 0.75
	rep := Analyze(recs, cfg, "t0", "t2", nil, true, true)
	found := false
	for _, g := range rep.Gates {
		if len(g.Name) >= 12 && g.Name[:12] == "missing-cost" && !g.Pass {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing-cost gate missing: %+v", rep.Gates)
	}
	if rep.Verdict != "NOT_RANKABLE" {
		t.Fatalf("verdict=%s want NOT_RANKABLE", rep.Verdict)
	}
}

// A5: commercial gate needs BOTH baselines: t2 beats t0 on the bar but not
// t1 -> INCONCLUSIVE with the bar reason.
func TestCommercialGateNeedsBothBaselines(t *testing.T) {
	at := fixtureBase
	var as []Assignment
	var evs []outcome.OutcomeEvent
	for i := 0; i < 4; i++ {
		for _, tc := range []struct {
			tx string
			c  float64
		}{{"t2", 1.0}, {"t0", 4.0}, {"t1", 1.1}} {
			j := tc.tx + itoa(i)
			as = append(as, fxAssign(j, tc.tx, at))
			evs = append(evs, fxEvent(j, tc.tx, 1, outcome.StatusAccepted, []float64{tc.c}, at))
		}
	}
	recs := MatureJobs(as, evs, at.Add(24*time.Hour), 24*time.Hour)
	cfg := testCfg()
	cfg.MinJobs = 4
	rep := Analyze(recs, cfg, "t0", "t2", []string{"t1"}, true, true)
	if len(rep.Comparisons) != 2 {
		t.Fatalf("comparisons=%d", len(rep.Comparisons))
	}
	if rep.Verdict != "INCONCLUSIVE" {
		t.Fatalf("verdict=%s reasons=%v (want INCONCLUSIVE)", rep.Verdict, rep.Reasons)
	}
	found := false
	for _, r := range rep.Reasons {
		if strings.Contains(r, "commercial bar") {
			found = true
		}
	}
	if !found {
		t.Fatalf("bar reason missing: %v", rep.Reasons)
	}
}
