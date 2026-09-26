package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// ObservedAttempt is what the runner measured on the wire for one attempt.
// It carries transport facts only — never a task verdict.
type ObservedAttempt struct {
	DecisionID string
	Arm        string
	Transport  outcome.TransportStatus
	HTTPStatus int
	// TimeoutMs is set when the client deadline fired (ambiguous outcome).
	TimeoutMs float64
}

// AttemptPlan is the verifier's verdict for one attempt: the full attempt
// record with independently determined task outcome.
type AttemptPlan struct {
	Attempt outcome.Attempt
	// TerminalSuccess ends the attempt loop when true.
	TerminalSuccess bool
}

// Verifier turns transport observations + workload truth into verified
// outcomes. Implementations must never derive task success from HTTP status:
// the fixture below decides from manifest truth, and any application
// verifier is audited the same way (a 200 with invalid output is REJECTED).
type Verifier interface {
	// PlanAttempt builds one attempt record from observation + truth.
	PlanAttempt(job ManifestJob, expID string, attIdx int, arm string, obs ObservedAttempt, assignedAt time.Time) AttemptPlan
	// PlanSettlement builds the version series for the finished attempt loop.
	// It returns Settle=false for unresolved jobs (nothing is submitted).
	PlanSettlement(job ManifestJob, expID string, firstDecision, jobID string, attempts []outcome.Attempt, assignedAt time.Time) (versions []outcome.OutcomeEvent, settle bool)
	Provenance() string
}

// drawSuccess is the deterministic ground-truth draw for (job, attempt, arm):
// sha256 over the experiment domain; order-independent, replay-identical.
// The manifest declares p; this function flips the coin. No runtime RNG.
func drawSuccess(expID, jobID string, attIdx int, arm string, p float64) bool {
	h := sha256.New()
	h.Write([]byte("exp-truth-v1\x00"))
	h.Write([]byte(expID))
	h.Write([]byte{0})
	h.Write([]byte(jobID))
	h.Write([]byte{0})
	var ib [8]byte
	binary.LittleEndian.PutUint64(ib[:], uint64(attIdx))
	h.Write(ib[:])
	h.Write([]byte{0})
	h.Write([]byte(arm))
	sum := h.Sum(nil)
	u := float64(binary.LittleEndian.Uint64(sum[:8])) / float64(1<<64)
	return u < p
}

// FixtureVerifier implements Verifier from manifest truth for dry runs.
// Provenance labels every verdict synthetic.
type FixtureVerifier struct{}

func (FixtureVerifier) Provenance() string { return "fixture:synthetic-v1" }

func (FixtureVerifier) PlanAttempt(job ManifestJob, expID string, attIdx int, arm string, obs ObservedAttempt, assignedAt time.Time) AttemptPlan {
	truth, ok := job.Arms[arm]
	if !ok {
		truth = ArmTruth{SuccessP: 0}
	}
	verified := outcome.VerifiedFailure
	validation := outcome.ValidationPass
	category := ""
	transport := obs.Transport
	latency := truth.LatencyMs
	cost := truth.CostUSD

	switch {
	case obs.Transport == outcome.TransportTimeout:
		// Ambiguous: the server may or may not have executed. Unknown until
		// an authoritative outcome resolves it — never a failure.
		verified = outcome.VerifiedUnknown
		validation = outcome.ValidationNotRun
		latency = obs.TimeoutMs
		cost = nil // unmetered: nothing observed, nothing imputed
	case obs.Transport != outcome.TransportOK:
		// A non-timeout transport error (e.g. HTTP 500, connection refused)
		// proves the attempt produced no usable output, but task outcome is
		// still decided by the independent truth draw below — never inferred
		// from the status. A 500 with a lucky draw verifies success exactly
		// as a 200 would; the transport field preserves what the wire said.
		verified = drawToVerified(drawSuccess(expID, job.JobID, attIdx, arm, truth.SuccessP))
		if verified == outcome.VerifiedFailure {
			category = "task_failure"
		}
	case job.Behavior == BehaviorInvalidOutput:
		// Transport succeeded (HTTP 200) but the payload is task-invalid.
		// This is the transport/task separation case: success on the wire,
		// failure on the merits.
		verified = outcome.VerifiedFailure
		category = "invalid_output"
	default:
		if drawSuccess(expID, job.JobID, attIdx, arm, truth.SuccessP) {
			verified = outcome.VerifiedSuccess
		} else {
			category = "task_failure"
		}
	}

	id := fmt.Sprintf("%s-a%d", job.JobID, attIdx)
	return AttemptPlan{
		Attempt: outcome.Attempt{
			AttemptID: id, Seq: uint(attIdx), ExecutorID: arm, ArmID: arm,
			Transport: transport, LatencyMs: latency, CostUSD: cost,
			Validation: validation, FailureCategory: category,
			Verified: verified, VerifiedBy: FixtureVerifier{}.Provenance(),
			VerifiedAt: assignedAt.UTC().Format(time.RFC3339Nano),
		},
		TerminalSuccess: verified == outcome.VerifiedSuccess,
	}
}

// PlanSettlement maps finished attempts to the version series. Human
// fallback appends a human attempt when configured and no model attempt
// succeeded; its cost travels via HumanReviewCostUSD, never as a model arm.
func (FixtureVerifier) PlanSettlement(job ManifestJob, expID string, firstDecision, jobID string, attempts []outcome.Attempt, assignedAt time.Time) ([]outcome.OutcomeEvent, bool) {
	mk := func(version uint64, status outcome.JobStatus, atts []outcome.Attempt, decider string, verifiedAt time.Time) outcome.OutcomeEvent {
		return outcome.OutcomeEvent{
			SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: firstDecision, JobID: jobID, StrategyID: "",
			Version: version, Supersedes: version - 1, Status: status,
			Attempts: atts, DecidingAttemptID: decider,
			HumanReviewCostUSD: humanCost(job, atts),
			VerifiedBy:         FixtureVerifier{}.Provenance(),
			VerifiedAt:         verifiedAt.UTC().Format(time.RFC3339Nano),
			OccurredAt:         verifiedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	withHuman := func(atts []outcome.Attempt) []outcome.Attempt {
		for _, a := range atts {
			if a.Verified == outcome.VerifiedSuccess {
				return atts
			}
		}
		if !job.Human.Enabled {
			return atts
		}
		out := append([]outcome.Attempt(nil), atts...)
		out = append(out, outcome.Attempt{
			AttemptID: job.JobID + "-ah", Seq: uint(len(atts)),
			ExecutorID: "human-pool", Transport: outcome.TransportOK,
			LatencyMs: job.Human.LatencyMs, Validation: outcome.ValidationPass,
			Verified: outcome.VerifiedSuccess, VerifiedBy: "human:fixture-pool",
			VerifiedAt: assignedAt.UTC().Format(time.RFC3339Nano),
		})
		return out
	}
	deciderOf := func(atts []outcome.Attempt, want outcome.VerifiedOutcome) string {
		for _, a := range atts {
			if a.Verified == want {
				return a.AttemptID
			}
		}
		if len(atts) > 0 {
			return atts[len(atts)-1].AttemptID
		}
		return ""
	}
	lag := time.Duration(job.VerifyLagH * float64(time.Hour))

	switch job.Behavior {
	case BehaviorUnresolved:
		return nil, false
	case BehaviorTimeoutThenAccept:
		v1 := mk(1, outcome.StatusUnknown, attempts, "", assignedAt)
		v1.CorrectedAt = ""
		fixed := withHuman(fixTimeoutAttempts(job, expID, attempts))
		if len(fixed) == 0 {
			v2 := mk(2, outcome.StatusUnknown, fixed, "", assignedAt.Add(lagOr(lag, 20*time.Hour)))
			v2.CorrectedAt = assignedAt.UTC().Format(time.RFC3339Nano)
			return []outcome.OutcomeEvent{v1, v2}, true
		}
		// The authoritative resolution follows the draw, not the behavior
		// name: a timeout that actually failed resolves REJECTED.
		if hasSuccess(fixed) {
			v2 := mk(2, outcome.StatusAccepted, fixed, deciderOf(fixed, outcome.VerifiedSuccess), assignedAt.Add(lagOr(lag, 20*time.Hour)))
			v2.CorrectedAt = assignedAt.UTC().Format(time.RFC3339Nano)
			return []outcome.OutcomeEvent{v1, v2}, true
		}
		v2 := mk(2, outcome.StatusRejected, fixed, deciderOf(fixed, outcome.VerifiedFailure), assignedAt.Add(lagOr(lag, 20*time.Hour)))
		v2.CorrectedAt = assignedAt.UTC().Format(time.RFC3339Nano)
		return []outcome.OutcomeEvent{v1, v2}, true
	case BehaviorUnknownThenAccept:
		v1 := mk(1, outcome.StatusUnknown, attempts, "", assignedAt)
		fixed := withHuman(fixTimeoutAttempts(job, expID, attempts))
		if len(fixed) == 0 {
			v2 := mk(2, outcome.StatusUnknown, fixed, "", assignedAt.Add(lagOr(lag, 20*time.Hour)))
			v2.CorrectedAt = assignedAt.UTC().Format(time.RFC3339Nano)
			return []outcome.OutcomeEvent{v1, v2}, true
		}
		if hasSuccess(fixed) {
			v2 := mk(2, outcome.StatusAccepted, fixed, deciderOf(fixed, outcome.VerifiedSuccess), assignedAt.Add(lagOr(lag, 20*time.Hour)))
			v2.CorrectedAt = assignedAt.UTC().Format(time.RFC3339Nano)
			return []outcome.OutcomeEvent{v1, v2}, true
		}
		v2 := mk(2, outcome.StatusRejected, fixed, deciderOf(fixed, outcome.VerifiedFailure), assignedAt.Add(lagOr(lag, 20*time.Hour)))
		v2.CorrectedAt = assignedAt.UTC().Format(time.RFC3339Nano)
		return []outcome.OutcomeEvent{v1, v2}, true
	case BehaviorCorrectToReject:
		full := withHuman(attempts)
		v1 := mk(1, outcome.StatusAccepted, full, deciderOf(full, outcome.VerifiedSuccess), assignedAt)
		// Correction: the success is revoked; the deciding attempt becomes
		// the last failed model attempt (human tape preserved as diagnostic).
		v2atts := revokeSuccess(full)
		v2 := mk(2, outcome.StatusRejected, v2atts, deciderOf(v2atts, outcome.VerifiedFailure), assignedAt.Add(lagOr(lag, 20*time.Hour)))
		v2.CorrectedAt = assignedAt.UTC().Format(time.RFC3339Nano)
		return []outcome.OutcomeEvent{v1, v2}, true
	case BehaviorDelayedAccept:
		// PENDING first (no verdict yet), terminal after the lag.
		v1 := mk(1, outcome.StatusPending, attempts, "", assignedAt)
		full := withHuman(attempts)
		v2 := mk(2, outcome.StatusAccepted, full, deciderOf(full, outcome.VerifiedSuccess), assignedAt.Add(lagOr(lag, 20*time.Hour)))
		v2.CorrectedAt = assignedAt.UTC().Format(time.RFC3339Nano)
		return []outcome.OutcomeEvent{v1, v2}, true
	default:
		full := withHuman(attempts)
		if d := deciderOf(full, outcome.VerifiedSuccess); d != "" && hasSuccess(full) {
			return []outcome.OutcomeEvent{mk(1, outcome.StatusAccepted, full, d, assignedAt.Add(lag))}, true
		}
		return []outcome.OutcomeEvent{mk(1, outcome.StatusRejected, full, deciderOf(full, outcome.VerifiedFailure), assignedAt.Add(lag))}, true
	}
}

// drawToVerified maps a ground-truth draw to a task verdict.
func drawToVerified(ok bool) outcome.VerifiedOutcome {
	if ok {
		return outcome.VerifiedSuccess
	}
	return outcome.VerifiedFailure
}

// fixTimeoutAttempts re-verifies timeout attempts once authoritative evidence
// exists, using the same deterministic draw the live attempt would have used
// (indexed by the attempt's sequence number, which the outcome contract
// guarantees equals its position). No threshold shortcuts: a timeout on a
// high-quality arm can still have failed, and the coin decides.
func fixTimeoutAttempts(job ManifestJob, expID string, attempts []outcome.Attempt) []outcome.Attempt {
	out := make([]outcome.Attempt, len(attempts))
	copy(out, attempts)
	for i := range out {
		if out[i].Verified != outcome.VerifiedUnknown {
			continue
		}
		p := job.Arms[out[i].ArmID].SuccessP
		if drawSuccess(expID, job.JobID, int(out[i].Seq), out[i].ArmID, p) {
			out[i].Verified = outcome.VerifiedSuccess
			out[i].Validation = outcome.ValidationPass
			out[i].VerifiedBy = "checker:reconciler-fixture"
		} else {
			out[i].Verified = outcome.VerifiedFailure
			out[i].FailureCategory = "task_failure"
			out[i].VerifiedBy = "checker:reconciler-fixture"
		}
	}
	return out
}

func revokeSuccess(atts []outcome.Attempt) []outcome.Attempt {
	out := make([]outcome.Attempt, len(atts))
	copy(out, atts)
	for i := range out {
		if out[i].Verified == outcome.VerifiedSuccess && out[i].ExecutorID != "human-pool" {
			out[i].Verified = outcome.VerifiedFailure
			out[i].FailureCategory = "revoked_on_review"
			out[i].VerifiedBy = "checker:review-fixture"
		}
	}
	return out
}

func hasSuccess(atts []outcome.Attempt) bool {
	for _, a := range atts {
		if a.Verified == outcome.VerifiedSuccess {
			return true
		}
	}
	return false
}

func humanCost(job ManifestJob, atts []outcome.Attempt) *float64 {
	for _, a := range atts {
		if a.ExecutorID == "human-pool" {
			c := job.Human.CostUSD
			return &c
		}
	}
	return nil
}

func lagOr(lag, def time.Duration) time.Duration {
	if lag > 0 {
		return lag
	}
	return def
}
