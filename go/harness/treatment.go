// Package harness — randomized treatment assignment and isolated execution.
//
// This file implements the experimental layer from PR3B_EXPERIMENT_SPEC.md:
// concurrent per-job randomization (T0 fixed, T1 cheapest-qualified static,
// T2 cost-blind Thompson), assignment persistence before execution, and
// fully isolated per-treatment state (policy, learner, files, checkpoints).
//
// Assignment uses a dedicated hash domain ("treatment-assignment-v1") keyed
// by (seed, strata, job_id). It never consumes the bandit RNG: experimental
// randomization and Thompson sampling draw from independent streams, and
// Assign is a pure function of the job ID, so retries and restarts reproduce
// the same treatment without stored RNG state.
package harness

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Assigner randomizes jobs to treatments deterministically.
type Assigner struct {
	Seed       uint64
	Treatments []string
	Weights    []float64
}

// Assignment is one persisted randomization outcome, written before the job
// executes so restarts and retries reproduce it.
type Assignment struct {
	JobID          string  `json:"job_id"`
	Strata         string  `json:"strata"`
	Treatment      string  `json:"treatment"`
	Probability    float64 `json:"probability"`
	AssignedAt     string  `json:"assigned_at"`
	AssignerSeed   uint64  `json:"assigner_seed"`
	AssignmentSeed string  `json:"assignment_seed"` // hash domain, always treatment-assignment-v1
}

const assignmentDomain = "treatment-assignment-v1"

// Assign draws a treatment for jobID. Pure: identical inputs always produce
// identical outputs, across processes and restarts.
func (a Assigner) Assign(jobID, strata string) (Assignment, error) {
	if len(a.Treatments) == 0 || len(a.Treatments) != len(a.Weights) {
		return Assignment{}, fmt.Errorf("harness: assigner needs equal-length treatments and weights")
	}
	total := 0.0
	for _, w := range a.Weights {
		if w < 0 {
			return Assignment{}, fmt.Errorf("harness: negative assignment weight")
		}
		total += w
	}
	if total <= 0 {
		return Assignment{}, fmt.Errorf("harness: non-positive total assignment weight")
	}
	h := sha256.New()
	h.Write([]byte(assignmentDomain))
	var sb [8]byte
	binary.LittleEndian.PutUint64(sb[:], a.Seed)
	h.Write(sb[:])
	h.Write([]byte{0})
	h.Write([]byte(strata))
	h.Write([]byte{0})
	h.Write([]byte(jobID))
	sum := h.Sum(nil)
	u := float64(binary.LittleEndian.Uint64(sum[:8])) / float64(1<<64)
	cum := 0.0
	for i, id := range a.Treatments {
		cum += a.Weights[i] / total
		if u < cum || i == len(a.Treatments)-1 {
			return Assignment{
				JobID: jobID, Strata: strata, Treatment: id,
				Probability:    a.Weights[i] / total,
				AssignerSeed:   a.Seed,
				AssignmentSeed: assignmentDomain,
			}, nil
		}
	}
	return Assignment{}, fmt.Errorf("harness: assignment unreachable")
}

// JobTruth is synthetic ground truth for dry runs and fixtures. It is
// explicitly NOT historical workload data: every field is either configured
// in the test or generated from a stated distribution.
type JobTruth struct {
	JobID      string
	Strata     string
	AssignedAt time.Time
	// Per-arm verified-success probability, cost, and latency of one attempt.
	ArmSuccess map[string]float64
	ArmCost    map[string]float64
	ArmLatency map[string]float64
	// HumanFallback, when set, appends a human attempt after model attempts
	// fail: always verifies success at the stated review cost.
	HumanFallback bool
	HumanCost     float64
}

// Strategy executes one job against its truth and returns the attempt tape.
// Strategies never learn: T0/T1 are static by definition, and T2's learning
// happens once at settlement through the outcome learner, not inside Execute.
type Strategy interface {
	ID() string
	Execute(rng *rand.Rand, job JobTruth) []outcome.Attempt
}

// StaticStrategy tries fixed arms in order until verified success or budget.
type StaticStrategy struct {
	StrategyID string
	Arms       []string
	MaxRetries int
	Verifier   string
}

func (s StaticStrategy) ID() string { return s.StrategyID }

func (s StaticStrategy) Execute(rng *rand.Rand, job JobTruth) []outcome.Attempt {
	var attempts []outcome.Attempt
	tries := s.MaxRetries + 1
	if tries > len(s.Arms) {
		tries = len(s.Arms)
	}
	for i := 0; i < tries; i++ {
		arm := s.Arms[i]
		p := job.ArmSuccess[arm]
		ok := rng.Float64() < p
		verified := outcome.VerifiedFailure
		if ok {
			verified = outcome.VerifiedSuccess
		}
		cost := job.ArmCost[arm]
		attempts = append(attempts, outcome.Attempt{
			AttemptID: fmt.Sprintf("%s-a%d", job.JobID, i), Seq: uint(i),
			ExecutorID: arm, ArmID: arm,
			Transport: outcome.TransportOK, LatencyMs: job.ArmLatency[arm],
			CostUSD: &cost, Validation: outcome.ValidationPass, Verified: verified,
			VerifiedBy: s.Verifier,
		})
		if ok {
			return attempts
		}
	}
	if job.HumanFallback {
		attempts = append(attempts, outcome.Attempt{
			AttemptID: fmt.Sprintf("%s-ah", job.JobID), Seq: uint(len(attempts)),
			ExecutorID: "human-pool", Transport: outcome.TransportOK,
			LatencyMs: 600000, Validation: outcome.ValidationPass,
			Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool",
			// No attempt-level cost: accounted via HumanReviewCostUSD.
		})
	}
	return attempts
}

// ThompsonStrategy selects arms from a live policy without learning inside
// Execute: one selection per attempt, settlement learns at most once.
type ThompsonStrategy struct {
	StrategyID string
	Policy     *thompson.Policy
	MaxRetries int
	Verifier   string
}

func (s ThompsonStrategy) ID() string { return s.StrategyID }

func (s ThompsonStrategy) Execute(rng *rand.Rand, job JobTruth) []outcome.Attempt {
	var attempts []outcome.Attempt
	for i := 0; i <= s.MaxRetries; i++ {
		arm, err := s.Policy.Select(rng)
		if err != nil {
			break
		}
		p := job.ArmSuccess[arm]
		ok := rng.Float64() < p
		verified := outcome.VerifiedFailure
		if ok {
			verified = outcome.VerifiedSuccess
		}
		cost := job.ArmCost[arm]
		attempts = append(attempts, outcome.Attempt{
			AttemptID: fmt.Sprintf("%s-a%d", job.JobID, i), Seq: uint(i),
			ExecutorID: arm, ArmID: arm,
			Transport: outcome.TransportOK, LatencyMs: job.ArmLatency[arm],
			CostUSD: &cost, Validation: outcome.ValidationPass, Verified: verified,
			VerifiedBy: s.Verifier,
		})
		if ok {
			return attempts
		}
	}
	if job.HumanFallback {
		attempts = append(attempts, outcome.Attempt{
			AttemptID: fmt.Sprintf("%s-ah", job.JobID), Seq: uint(len(attempts)),
			ExecutorID: "human-pool", Transport: outcome.TransportOK,
			LatencyMs: 600000, Validation: outcome.ValidationPass,
			Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool",
		})
	}
	return attempts
}

// TreatmentInstance is one treatment's fully isolated state: its own policy
// (T2) or none (static), learner (T2), outcome ledger, assignment log, and
// checkpoint path. Nothing is shared with sibling treatments.
type TreatmentInstance struct {
	ID         string
	Dir        string
	Policy     *thompson.Policy // nil for static treatments
	Learner    *outcome.Learner // nil for static treatments
	Store      *outcome.FileOutcomeStore
	assignFile *os.File
	mu         sync.Mutex
}

// OpenTreatment creates an isolated treatment directory layout. Static
// treatments pass learn=false and get accounting without learning.
func OpenTreatment(dir, id string, policy *thompson.Policy, learn bool) (*TreatmentInstance, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	store, err := outcome.NewFileOutcomeStore(dir + "/outcomes.jsonl")
	if err != nil {
		return nil, err
	}
	af, err := os.OpenFile(dir+"/assignments.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	t := &TreatmentInstance{ID: id, Dir: dir, Policy: policy, Store: store, assignFile: af}
	if learn {
		if policy == nil {
			_ = store.Close()
			_ = af.Close()
			return nil, fmt.Errorf("harness: treatment %s learns but has no policy", id)
		}
		t.Learner = outcome.NewLearner(policy, outcome.BinaryStatusMapper{}, store.Events)
	}
	return t, nil
}

func (t *TreatmentInstance) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var first error
	if t.assignFile != nil {
		if err := t.assignFile.Sync(); err != nil && first == nil {
			first = err
		}
		if err := t.assignFile.Close(); err != nil && first == nil {
			first = err
		}
		t.assignFile = nil
	}
	if err := t.Store.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// RunJob persists the assignment BEFORE execution, runs the strategy, then
// settles v1: T2 learns through the learner, static treatments ledger only.
func (t *TreatmentInstance) RunJob(rng *rand.Rand, strategy Strategy, job JobTruth, a Assignment) (outcome.OutcomeEvent, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a.AssignedAt = job.AssignedAt.UTC().Format(time.RFC3339Nano)
	ab, err := json.Marshal(a)
	if err != nil {
		return outcome.OutcomeEvent{}, err
	}
	ab = append(ab, '\n')
	if _, err := t.assignFile.Write(ab); err != nil {
		return outcome.OutcomeEvent{}, err
	}
	if err := t.assignFile.Sync(); err != nil {
		return outcome.OutcomeEvent{}, err
	}

	attempts := strategy.Execute(rng, job)
	decider, status := decide(attempts)
	var humanCost *float64
	for _, at := range attempts {
		if at.ExecutorID == "human-pool" {
			hc := job.HumanCost
			humanCost = &hc
		}
	}
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: "dec-" + job.JobID, JobID: job.JobID, StrategyID: t.ID,
		Version: 1, Supersedes: 0, Status: status, Attempts: attempts,
		DecidingAttemptID: decider, HumanReviewCostUSD: humanCost,
		VerifiedBy: "harness:synthetic-v1",
		VerifiedAt: job.AssignedAt.UTC().Format(time.RFC3339Nano),
		OccurredAt: job.AssignedAt.UTC().Format(time.RFC3339Nano),
	}
	if t.Learner != nil {
		if _, err := outcome.Settle(t.Store, t.Learner, ev); err != nil {
			return outcome.OutcomeEvent{}, err
		}
	} else {
		if _, err := t.Store.Submit(ev); err != nil {
			return outcome.OutcomeEvent{}, err
		}
	}
	return ev, nil
}

// decide derives the terminal status: first verified success ACCEPTs;
// exhausted attempts without success REJECTs. No verified signal at all
// (empty tape) is UNKNOWN — never invented.
func decide(attempts []outcome.Attempt) (string, outcome.JobStatus) {
	for _, a := range attempts {
		if a.Verified == outcome.VerifiedSuccess {
			return a.AttemptID, outcome.StatusAccepted
		}
	}
	if len(attempts) == 0 {
		return "", outcome.StatusUnknown
	}
	return attempts[len(attempts)-1].AttemptID, outcome.StatusRejected
}

// RequiredPerGroup returns the per-treatment job count for a two-sample
// comparison of means (normal approximation): n = 2σ²(z_α + z_β)²/δ² with
// δ = relEffect × baselineMean. It sizes data collection; it is not evidence.
func RequiredPerGroup(stddev, baselineMean, relEffect, alpha, power float64) float64 {
	delta := relEffect * baselineMean
	if delta == 0 || stddev <= 0 {
		return 0
	}
	za := normalQuantile(1 - alpha/2)
	zb := normalQuantile(power)
	n := 2 * stddev * stddev * (za + zb) * (za + zb) / (delta * delta)
	return n
}

// normalQuantile approximates Φ⁻¹(p) (Acklam's approximation, ~1e-9).
func normalQuantile(p float64) float64 {
	if p <= 0 || p >= 1 {
		return 0
	}
	a := []float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02, 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := []float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02, 6.680131188771972e+01, -1.328068155288572e+01}
	c := []float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00, -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := []float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00}
	plow, phigh := 0.02425, 1-0.02425
	var q, r float64
	switch {
	case p < plow:
		q = math.Sqrt(-2 * math.Log(p))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	case p > phigh:
		q = math.Sqrt(-2 * math.Log(1-p))
		return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	default:
		q = p - 0.5
		r = q * q
		return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q / (((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
	}
}
