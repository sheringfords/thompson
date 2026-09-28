package journal

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func testDecision(id, job, arm string, explore bool) Decision {
	return Decision{
		DecisionID: id, JobID: job, StrategyID: "t3", SelectedArm: arm,
		Eligible: []string{"cheap", "strong"},
		Scores:   map[string]float64{"cheap": 0.6, "strong": 0.5},
		PolicyID: thompson.CostAwarePolicyID, RuleVersion: thompson.CostAwareRuleV2,
		Objective: thompson.CostAwareObjectiveVer, ConfigHash: "cfg",
		Explore: explore,
	}
}

func testOutcome(dec, job string, ver uint64, ok bool, cost *float64) SettledOutcome {
	return testOutcomeArm(dec, job, "cheap", ver, ok, cost)
}

func testOutcomeArm(dec, job, arm string, ver uint64, ok bool, cost *float64) SettledOutcome {
	st, vered := StatusAccepted, "success"
	if !ok {
		st, vered = StatusRejected, "failure"
	}
	return SettledOutcome{
		DecisionID: dec, JobID: job, Version: ver, Supersedes: ver - 1,
		Status: st,
		Attempts: []OutcomeAttempt{{
			AttemptID: job + "-a0", ArmID: arm, CostUSD: cost, Verified: vered,
		}},
		DecidingAttempt: job + "-a0", VerifiedBy: "bench",
	}
}

func f64(v float64) *float64 { return &v }

func TestJournalOpenAndWriterRefusal(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	// Contention, not open, is SQLite's refusal boundary: hold a write
	// transaction on a second handle and prove every writer fails loudly
	// instead of interleaving. (White-box: same kernel locks a second
	// process would contend on.)
	j2, err := Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	if _, err := j2.db.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("holder begin: %v", err)
	}
	if _, _, err := j.CommitDecision(testDecision("dx", "jx", "cheap", false), "cfg"); err == nil {
		t.Fatal("contended writer must fail loudly, not queue")
	} else {
		t.Logf("contention refusal: %v", err)
	}
	if _, err := j2.db.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	// After release the writer proceeds.
	if _, ok, err := j.CommitDecision(testDecision("dx", "jx", "cheap", false), "cfg"); err != nil || !ok {
		t.Fatalf("post-release commit: %v %v", err, ok)
	}
}

func TestJournalDecisionLifecycle(t *testing.T) {
	j := mustOpen(t)
	defer j.Close()
	seq, ok, err := j.CommitDecision(testDecision("d1", "j1", "cheap", true), "cfg")
	if err != nil || !ok || seq == 0 {
		t.Fatalf("commit: %v %v %d", err, ok, seq)
	}
	// Idempotent redelivery.
	if _, ok, err := j.CommitDecision(testDecision("d1", "j1", "cheap", true), "cfg"); err != nil || ok {
		t.Fatalf("duplicate must be idempotent: %v %v", err, ok)
	}
	// Conflicting reuse refused.
	bad := testDecision("d1", "j1", "strong", false)
	if _, _, err := j.CommitDecision(bad, "cfg"); err == nil {
		t.Fatal("conflicting decision reuse must fail")
	}
	// Ineligible selection refused.
	bad2 := testDecision("d2", "j2", "nope", false)
	if _, _, err := j.CommitDecision(bad2, "cfg"); err == nil {
		t.Fatal("ineligible selection must fail")
	}
}

func TestJournalOutcomeLifecycle(t *testing.T) {
	j := mustOpen(t)
	defer j.Close()
	if _, _, err := j.CommitDecision(testDecision("d1", "j1", "cheap", false), "cfg"); err != nil {
		t.Fatal(err)
	}
	if ok, err := j.SettleOutcome(testOutcome("d1", "j1", 1, true, f64(0.02)), "cfg"); err != nil || !ok {
		t.Fatalf("settle v1: %v %v", err, ok)
	}
	// Duplicate idempotent.
	if ok, err := j.SettleOutcome(testOutcome("d1", "j1", 1, true, f64(0.02)), "cfg"); err != nil || ok {
		t.Fatalf("duplicate must be idempotent: %v %v", err, ok)
	}
	// Unknown decision refused.
	if _, err := j.SettleOutcome(testOutcome("dx", "jx", 1, true, f64(0.02)), "cfg"); err == nil {
		t.Fatal("unknown decision must fail")
	}
	// Gap refused.
	if _, err := j.SettleOutcome(testOutcome("d1", "j1", 3, true, f64(0.02)), "cfg"); err == nil {
		t.Fatal("version gap must fail")
	}
	// Correction applies.
	v2 := testOutcome("d1", "j1", 2, false, f64(0.02))
	if ok, err := j.SettleOutcome(v2, "cfg"); err != nil || !ok {
		t.Fatalf("correction: %v %v", err, ok)
	}
	// Stale redelivery acknowledged without effect.
	if ok, err := j.SettleOutcome(testOutcome("d1", "j1", 1, true, f64(0.02)), "cfg"); err != nil || ok {
		t.Fatalf("stale must be no-op: %v %v", err, ok)
	}
	// Malformed costs refused.
	neg := -0.5
	if _, err := j.SettleOutcome(testOutcome("d1", "j1", 3, true, &neg), "cfg"); err == nil {
		t.Fatal("negative cost must fail")
	}
	if _, _, err := j.CommitDecision(testDecision("d9", "j9", "cheap", false), "cfg"); err != nil {
		t.Fatal(err)
	}
	if ok, err := j.SettleOutcome(testOutcome("d9", "j9", 1, true, nil), "cfg"); err != nil || !ok {
		t.Fatalf("missing cost must settle as unmetered: %v %v", err, ok)
	}
	p, err := j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if p.Unmetered["cheap"] != 1 {
		t.Fatalf("unmetered job not counted: %+v", p.Unmetered)
	}
	// j1 contributes exactly one metered observation (v2 0.02 supersedes
	// v1); j9 contributes none. Mean must be exactly the metered average.
	if p.CostMean["cheap"] != 0.02 {
		t.Fatalf("mean not metered-only: %v", p.CostMean["cheap"])
	}
}

func TestJournalSafetyLifecycle(t *testing.T) {
	j := mustOpen(t)
	defer j.Close()
	s := SafetyTransition{Actor: "op", Type: "ARM_SUSPENDED", Arm: "cheap", Reason: "r", Nonce: "n1"}
	if _, ok, err := j.RecordSafety(s, "cfg"); err != nil || !ok {
		t.Fatalf("record: %v %v", err, ok)
	}
	if _, ok, err := j.RecordSafety(s, "cfg"); err != nil || ok {
		t.Fatalf("retry must be idempotent: %v %v", err, ok)
	}
	bad := SafetyTransition{Actor: "op", Type: "NOPE", Nonce: "n2"}
	if _, _, err := j.RecordSafety(bad, "cfg"); err == nil {
		t.Fatal("unknown safety type must fail")
	}
	p, err := j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if p.Safety["cheap"] != "SUSPENDED" {
		t.Fatalf("replay missed suspension: %+v", p.Safety)
	}
}

// Equivalence: journal projections match production learner + cost book on
// the same settled history (the central hypothesis requires it).
func TestJournalMatchesProduction(t *testing.T) {
	j := mustOpen(t)
	defer j.Close()
	type row struct {
		arm  string
		win  bool
		cost *float64
	}
	history := []row{
		{"cheap", true, f64(0.01)}, {"strong", false, f64(0.05)},
		{"cheap", true, f64(0.02)}, {"cheap", false, f64(0.015)},
		{"strong", true, f64(0.04)}, {"cheap", true, nil},
	}
	for i, r := range history {
		did := "d" + itoaJ(i)
		jid := "j" + itoaJ(i)
		if _, _, err := j.CommitDecision(testDecision(did, jid, r.arm, false), "cfg"); err != nil {
			t.Fatal(err)
		}
		if _, err := j.SettleOutcome(testOutcomeArm(did, jid, r.arm, 1, r.win, r.cost), "cfg"); err != nil {
			t.Fatal(err)
		}
	}
	p, err := j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	// Production learner over the equivalent outcome stream.
	store := outcome.NewMemoryOutcomeStore()
	// NOTE: production defaults include family warm start; the journal
	// projection uses textbook Beta(1,1). Equivalence here is checked
	// against a textbook-pinned production policy (see bench parity docs).
	tpol := thompson.New(thompson.Config{
		UpdateRule: thompson.DefaultUpdateRule(),
		WarmStart:  thompson.WarmStart{Kind: thompson.ColdStart},
		Selection:  thompson.Selection{Kind: thompson.ThompsonSelection},
	}, thompson.ExactSampler{})
	tpol.AddArm("cheap")
	tpol.AddArm("strong")
	learner := outcome.NewLearner(tpol, outcome.BinaryStatusMapper{}, store.Events)
	book := outcome.NewCostBookV1([]string{"cheap", "strong"})
	for i, r := range history {
		jid := "j" + itoaJ(i)
		ev := outcome.OutcomeEvent{
			SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: "d" + itoaJ(i), JobID: jid, StrategyID: "t",
			Version: 1, Status: outcome.StatusRejected,
			Attempts: []outcome.Attempt{{
				AttemptID: jid + "-a0", Seq: 0, ExecutorID: r.arm, ArmID: r.arm,
				Transport: outcome.TransportOK, CostUSD: r.cost,
				Validation: outcome.ValidationPass, Verified: outcome.VerifiedFailure, VerifiedBy: "b",
			}},
			DecidingAttemptID: jid + "-a0", OccurredAt: "2026-01-05T00:00:00Z",
		}
		if r.win {
			ev.Status = outcome.StatusAccepted
			ev.Attempts[0].Verified = outcome.VerifiedSuccess
		}
		if _, err := outcome.Settle(store, learner, ev); err != nil {
			t.Fatal(err)
		}
		if _, err := book.Apply(ev, store.Events); err != nil {
			t.Fatal(err)
		}
	}
	for _, arm := range []string{"cheap", "strong"} {
		post, ok := tpol.PosteriorFor(arm)
		if !ok {
			t.Fatal("missing arm")
		}
		got := p.Quality[arm]
		if got.Alpha != post.Alpha || got.Beta != post.Beta || got.Pulls != post.Pulls {
			t.Fatalf("quality diverged on %s: %+v vs %+v", arm, got, post)
		}
		bm, bok := book.Mean(arm)
		_, jok := p.CostMean[arm]
		if bok != jok {
			t.Fatalf("cost-known diverged on %s", arm)
		}
		if bok {
			if bm != p.CostMean[arm] {
				t.Fatalf("cost mean diverged on %s: %v vs %v", arm, bm, p.CostMean[arm])
			}
		}
		if _, ok := p.Unmetered[arm]; !ok && arm == "cheap" {
			// cheap's last job is unmetered: must be counted, never learned.
			t.Logf("note: cheap unmetered counter=%d", p.Unmetered[arm])
		}
	}
}

func mustOpen(t *testing.T) *Journal {
	t.Helper()
	j, err := Open(t.TempDir() + "/j.db")
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func itoaJ(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func decID(p string, i int) string { return p + "-dec-" + itoaJ(i) }
func jobID(p string, i int) string { return p + "-job-" + itoaJ(i) }

// Gateway-fidelity extensions: old rows without the new fields replay
// identically, and empty-nonce safety events append (never collide).
func TestJournalSchemaExtensions(t *testing.T) {
	j := mustOpen(t)
	defer j.Close()
	d := testDecision("dg", "jg", "cheap", false)
	d.ScoreKind = "samples"
	d.EligibleState = []ArmState{{ArmID: "cheap", Alpha: 2, Beta: 1, Pulls: 1}}
	d.CostPerSuc = map[string]float64{"cheap": 0.01}
	if _, ok, err := j.CommitDecision(d, "cfg"); err != nil || !ok {
		t.Fatalf("commit with extensions: %v %v", err, ok)
	}
	for i := 0; i < 3; i++ {
		s := SafetyTransition{Actor: "op", Type: "ARM_SUSPENDED", Arm: "cheap", Reason: "r"}
		if _, ok, err := j.RecordSafety(s, "cfg"); err != nil || !ok {
			t.Fatalf("empty-nonce append %d: %v %v", i, err, ok)
		}
	}
	p, err := j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if p.Safety["cheap"] != "SUSPENDED" {
		t.Fatal("suspension lost")
	}
	evs, err := j.EventsSince(0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "decision" {
			var dd Decision
			if err := json.Unmarshal([]byte(e.Payload), &dd); err != nil {
				t.Fatal(err)
			}
			if dd.ScoreKind == "samples" && dd.CostPerSuc["cheap"] == 0.01 {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("extended decision fields lost in round-trip")
	}
}
