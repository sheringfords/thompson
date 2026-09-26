package main

import (
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

var vAt = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

func vJob(behavior JobBehavior) ManifestJob {
	c := 0.01
	return ManifestJob{
		JobID: "vj", Strata: "s", Behavior: behavior,
		Arms: map[string]ArmTruth{"a": {SuccessP: 1.0, CostUSD: &c, LatencyMs: 100}},
	}
}

func vObs(t outcome.TransportStatus) ObservedAttempt {
	return ObservedAttempt{DecisionID: "d", Arm: "a", Transport: t, HTTPStatus: 200}
}

// Timeout is unknown with unmetered cost, never a failure.
func TestPlanAttemptTimeoutUnknown(t *testing.T) {
	p := FixtureVerifier{}.PlanAttempt(vJob(BehaviorNormal), "e", 0, "a",
		ObservedAttempt{DecisionID: "d", Transport: outcome.TransportTimeout, TimeoutMs: 500}, vAt)
	if p.Attempt.Verified != outcome.VerifiedUnknown {
		t.Fatalf("timeout verified as %q", p.Attempt.Verified)
	}
	if p.Attempt.CostUSD != nil {
		t.Fatal("timeout cost imputed (must stay unknown)")
	}
	if p.TerminalSuccess {
		t.Fatal("unknown terminates the loop")
	}
}

// Invalid output: HTTP 200 on the wire, task failure on the merits.
func TestPlanAttemptInvalidOutputRejected(t *testing.T) {
	p := FixtureVerifier{}.PlanAttempt(vJob(BehaviorInvalidOutput), "e", 0, "a", vObs(outcome.TransportOK), vAt)
	if p.Attempt.Verified != outcome.VerifiedFailure || p.Attempt.FailureCategory != "invalid_output" {
		t.Fatalf("200-with-garbage not rejected: %+v", p.Attempt)
	}
}

// Determinism: identical inputs reproduce identical verdicts.
func TestPlanAttemptDeterministic(t *testing.T) {
	job := vJob(BehaviorNormal)
	job.Arms["a"] = ArmTruth{SuccessP: 0.5, CostUSD: f64p(0.01), LatencyMs: 100}
	a := FixtureVerifier{}.PlanAttempt(job, "e", 3, "a", vObs(outcome.TransportOK), vAt)
	b := FixtureVerifier{}.PlanAttempt(job, "e", 3, "a", vObs(outcome.TransportOK), vAt)
	if a.Attempt.Verified != b.Attempt.Verified {
		t.Fatal("verifier not deterministic")
	}
}

func settleOnly(t *testing.T, job ManifestJob, attempts []outcome.Attempt) ([]outcome.OutcomeEvent, bool) {
	t.Helper()
	return FixtureVerifier{}.PlanSettlement(job, "e", "dec", "job-dec", attempts, vAt)
}

func TestSettlementTrajectories(t *testing.T) {
	okAtt := outcome.Attempt{AttemptID: "vj-a0", Seq: 0, ExecutorID: "a", ArmID: "a",
		Transport: outcome.TransportOK, LatencyMs: 100, Verified: outcome.VerifiedSuccess}
	failAtt := outcome.Attempt{AttemptID: "vj-a0", Seq: 0, ExecutorID: "a", ArmID: "a",
		Transport: outcome.TransportOK, LatencyMs: 100, Verified: outcome.VerifiedFailure}

	// Normal accept / reject.
	if vs, ok := settleOnly(t, vJob(BehaviorNormal), []outcome.Attempt{okAtt}); !ok || len(vs) != 1 || vs[0].Status != outcome.StatusAccepted || vs[0].DecidingAttemptID != "vj-a0" {
		t.Fatalf("accept: %+v %v", vs, ok)
	}
	if vs, ok := settleOnly(t, vJob(BehaviorNormal), []outcome.Attempt{failAtt}); !ok || len(vs) != 1 || vs[0].Status != outcome.StatusRejected {
		t.Fatalf("reject: %+v %v", vs, ok)
	}
	// Unresolved settles nothing.
	if _, ok := settleOnly(t, vJob(BehaviorUnresolved), []outcome.Attempt{failAtt}); ok {
		t.Fatal("unresolved produced an outcome (fabrication)")
	}
	// Timeout → UNKNOWN v1, ACCEPTED v2.
	toAtt := outcome.Attempt{AttemptID: "vj-a0", Seq: 0, ExecutorID: "a", ArmID: "a",
		Transport: outcome.TransportTimeout, LatencyMs: 500, Verified: outcome.VerifiedUnknown}
	vs, ok := settleOnly(t, vJob(BehaviorTimeoutThenAccept), []outcome.Attempt{toAtt})
	if !ok || len(vs) != 2 || vs[0].Status != outcome.StatusUnknown || vs[1].Status != outcome.StatusAccepted {
		t.Fatalf("timeout-then-accept: %+v %v", vs, ok)
	}
	if vs[1].Version != 2 || vs[1].Supersedes != 1 {
		t.Fatalf("bad version chain: %+v", vs[1])
	}
	// Delayed: PENDING then ACCEPTED.
	vs, ok = settleOnly(t, vJob(BehaviorDelayedAccept), []outcome.Attempt{failAtt})
	if !ok || len(vs) != 2 || vs[0].Status != outcome.StatusPending || vs[1].Status != outcome.StatusAccepted {
		t.Fatalf("delayed: %+v %v", vs, ok)
	}
	// Correction: ACCEPTED then REJECTED, human tape preserved.
	vs, ok = settleOnly(t, vJob(BehaviorCorrectToReject), []outcome.Attempt{okAtt})
	if !ok || len(vs) != 2 || vs[0].Status != outcome.StatusAccepted || vs[1].Status != outcome.StatusRejected {
		t.Fatalf("correction: %+v %v", vs, ok)
	}
	// Unknown then accept.
	vs, ok = settleOnly(t, vJob(BehaviorUnknownThenAccept), []outcome.Attempt{toAtt})
	if !ok || len(vs) != 2 || vs[0].Status != outcome.StatusUnknown || vs[1].Status != outcome.StatusAccepted {
		t.Fatalf("unknown-then-accept: %+v %v", vs, ok)
	}
}

// Human fallback appends a human attempt with cost via HumanReviewCostUSD,
// never as a model arm.
func TestHumanFallbackAccounting(t *testing.T) {
	job := vJob(BehaviorNormal)
	job.Human = HumanTruth{Enabled: true, CostUSD: 2.5, LatencyMs: 600000, AlwaysSucceed: true}
	failAtt := outcome.Attempt{AttemptID: "vj-a0", Seq: 0, ExecutorID: "a", ArmID: "a",
		Transport: outcome.TransportOK, LatencyMs: 100, Verified: outcome.VerifiedFailure}
	vs, ok := settleOnly(t, job, []outcome.Attempt{failAtt})
	if !ok || len(vs) != 1 || vs[0].Status != outcome.StatusAccepted {
		t.Fatalf("human recovery: %+v %v", vs, ok)
	}
	if len(vs[0].Attempts) != 2 || vs[0].Attempts[1].ExecutorID != "human-pool" {
		t.Fatalf("human attempt missing: %+v", vs[0].Attempts)
	}
	if vs[0].HumanReviewCostUSD == nil || *vs[0].HumanReviewCostUSD != 2.5 {
		t.Fatalf("human cost wrong: %+v", vs[0].HumanReviewCostUSD)
	}
	if vs[0].DecidingAttemptID == "vj-a0" {
		t.Fatal("failed model attempt recorded as decider")
	}
}

// D2: non-timeout transport errors do not decide the task outcome. With
// SuccessP=1 the verdict is success despite the 500; with 0 it is failure.
// Either way the transport field preserves what the wire said.
func TestTransportErrorDoesNotDecideOutcome(t *testing.T) {
	mk := func(p float64) ManifestJob {
		return ManifestJob{JobID: "te", Strata: "s", Behavior: BehaviorNormal,
			Arms: map[string]ArmTruth{"a": {SuccessP: p, LatencyMs: 100}}}
	}
	err500 := ObservedAttempt{DecisionID: "d", Arm: "a", Transport: outcome.TransportError, HTTPStatus: 500}
	good := FixtureVerifier{}.PlanAttempt(mk(1.0), "e", 0, "a", err500, vAt)
	if good.Attempt.Verified != outcome.VerifiedSuccess {
		t.Fatalf("500 with certain truth not success: %+v", good.Attempt)
	}
	if good.Attempt.Transport != outcome.TransportError {
		t.Fatal("transport observation overwritten")
	}
	bad := FixtureVerifier{}.PlanAttempt(mk(0.0), "e", 0, "a", err500, vAt)
	if bad.Attempt.Verified != outcome.VerifiedFailure {
		t.Fatalf("500 with impossible truth not failure: %+v", bad.Attempt)
	}
}

// D2: timeout resolution follows the deterministic draw, not a threshold.
// With SuccessP=1 every timeout resolves success; with 0, failure.
func TestTimeoutResolutionDrawBased(t *testing.T) {
	mk := func(p float64) ManifestJob {
		return ManifestJob{JobID: "tr", Strata: "s", Behavior: BehaviorTimeoutThenAccept,
			Arms: map[string]ArmTruth{"a": {SuccessP: p, LatencyMs: 100}}}
	}
	toAtt := func() []outcome.Attempt {
		return []outcome.Attempt{{
			AttemptID: "tr-a0", Seq: 0, ExecutorID: "a", ArmID: "a",
			Transport: outcome.TransportTimeout, LatencyMs: 500,
			Validation: outcome.ValidationNotRun, Verified: outcome.VerifiedUnknown,
		}}
	}
	vs, ok := FixtureVerifier{}.PlanSettlement(mk(1.0), "e", "dec", "job-dec", toAtt(), vAt)
	if !ok || len(vs) != 2 || vs[1].Status != outcome.StatusAccepted {
		t.Fatalf("certain timeout must resolve accept: %+v %v", vs, ok)
	}
	vs, ok = FixtureVerifier{}.PlanSettlement(mk(0.0), "e", "dec", "job-dec", toAtt(), vAt)
	if !ok || len(vs) != 2 {
		t.Fatalf("impossible timeout plan wrong: %+v %v", vs, ok)
	}
	// p=0: timeout resolves to failure; with no human fallback the job
	// rejects rather than accepts.
	if vs[1].Status != outcome.StatusRejected {
		t.Fatalf("impossible timeout must resolve reject: %+v", vs[1])
	}
}
