package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// Manifest is the versioned workload input. workload_version is the sha256
// of the canonical manifest content (every field except workload_version
// itself); runners refuse a manifest whose version does not match, so the
// analyzed workload is exactly the declared one.
type Manifest struct {
	ExperimentID string            `json:"experiment_id"`
	WorkloadName string            `json:"workload_name"`
	Seed         uint64            `json:"seed"`
	MaturationH  float64           `json:"maturation_hours"`
	Treatments   []TreatmentConfig `json:"treatments"`
	Jobs         []ManifestJob     `json:"jobs"`
	// CharterDigest pins the frozen experimental charter this workload was
	// built for. Resume binds to it: a manifest authorized under a different
	// charter is a different experiment and must not continue this ledger.
	CharterDigest string `json:"charter_digest,omitempty"`

	WorkloadVersion string `json:"workload_version"`
	Synthetic       bool   `json:"synthetic"`
}

// TreatmentConfig binds a treatment ID to its strategy and gateway wiring.
type TreatmentConfig struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Arms        []string `json:"arms"`
	MaxAttempts int      `json:"max_attempts"`
	Learn       bool     `json:"learn"`
	// GatewayDescribe only: ports and files are runner flags, not manifest.
}

// ArmTruth is the workload-supplied measurement model for one arm. Costs and
// latencies are recorded measurements from the workload author; a nil cost
// stays unknown through the whole pipeline (never zero-filled).
type ArmTruth struct {
	SuccessP  float64  `json:"success_p"`
	CostUSD   *float64 `json:"cost_usd"`
	LatencyMs float64  `json:"latency_ms"`
}

// HumanTruth models a human fallback step when present.
type HumanTruth struct {
	Enabled       bool    `json:"enabled"`
	CostUSD       float64 `json:"cost_usd"`
	LatencyMs     float64 `json:"latency_ms"`
	AlwaysSucceed bool    `json:"always_succeed"`
}

// JobBehavior selects the verification trajectory for one job.
type JobBehavior string

const (
	BehaviorNormal            JobBehavior = "normal"
	BehaviorInvalidOutput     JobBehavior = "invalid-output"
	BehaviorTimeoutThenAccept JobBehavior = "timeout-then-accept"
	BehaviorUnresolved        JobBehavior = "unresolved"
	BehaviorDelayedAccept     JobBehavior = "delayed-accept"
	BehaviorCorrectToReject   JobBehavior = "correct-to-reject"
	BehaviorUnknownThenAccept JobBehavior = "unknown-then-accept"
)

// ManifestJob is one unit of work plus its ground truth.
type ManifestJob struct {
	JobID      string              `json:"job_id"`
	Strata     string              `json:"strata"`
	Eligible   []string            `json:"eligible,omitempty"`
	Arms       map[string]ArmTruth `json:"arms"`
	Human      HumanTruth          `json:"human,omitempty"`
	Behavior   JobBehavior         `json:"behavior"`
	VerifyLagH float64             `json:"verify_lag_hours,omitempty"`
}

// LoadManifest reads, validates, and version-checks a manifest file.
func LoadManifest(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("exp-run: bad manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	// Version check last: the declared version must match content.
	sum, err := m.contentHash()
	if err != nil {
		return nil, err
	}
	if m.WorkloadVersion != "" && m.WorkloadVersion != sum {
		return nil, fmt.Errorf("exp-run: workload_version mismatch: declared %q computed %q",
			m.WorkloadVersion, sum)
	}
	m.WorkloadVersion = sum
	return &m, nil
}

// Validate checks structural invariants, including one-treatment-per-job
// eligibility (a job listed twice or eligible nowhere is rejected).
func (m *Manifest) Validate() error {
	if m.ExperimentID == "" {
		return fmt.Errorf("exp-run: experiment_id required")
	}
	if len(m.Treatments) == 0 {
		return fmt.Errorf("exp-run: no treatments")
	}
	known := map[string]bool{}
	for _, t := range m.Treatments {
		if t.ID == "" {
			return fmt.Errorf("exp-run: treatment with empty id")
		}
		if known[t.ID] {
			return fmt.Errorf("exp-run: duplicate treatment %q", t.ID)
		}
		known[t.ID] = true
		if len(t.Arms) == 0 {
			return fmt.Errorf("exp-run: treatment %q has no arms", t.ID)
		}
		if t.MaxAttempts < 1 {
			return fmt.Errorf("exp-run: treatment %q max_attempts < 1", t.ID)
		}
	}
	seen := map[string]bool{}
	for i := range m.Jobs {
		j := &m.Jobs[i]
		if j.JobID == "" {
			return fmt.Errorf("exp-run: job %d has empty job_id", i)
		}
		if seen[j.JobID] {
			return fmt.Errorf("exp-run: duplicate job_id %q (one job enters one treatment)", j.JobID)
		}
		seen[j.JobID] = true
		if len(j.Arms) == 0 {
			return fmt.Errorf("exp-run: job %q has no arm truth", j.JobID)
		}
		for arm, at := range j.Arms {
			if at.SuccessP < 0 || at.SuccessP > 1 {
				return fmt.Errorf("exp-run: job %q arm %q success_p out of range", j.JobID, arm)
			}
			if at.LatencyMs < 0 || (at.CostUSD != nil && *at.CostUSD < 0) {
				return fmt.Errorf("exp-run: job %q arm %q negative measurement", j.JobID, arm)
			}
		}
		if elig := j.Eligible; len(elig) > 0 {
			for _, e := range elig {
				if !known[e] {
					return fmt.Errorf("exp-run: job %q eligible for unknown treatment %q", j.JobID, e)
				}
			}
		}
		switch j.Behavior {
		case "", BehaviorNormal:
			j.Behavior = BehaviorNormal
		case BehaviorInvalidOutput, BehaviorTimeoutThenAccept, BehaviorUnresolved,
			BehaviorDelayedAccept, BehaviorCorrectToReject, BehaviorUnknownThenAccept:
		default:
			return fmt.Errorf("exp-run: job %q unknown behavior %q", j.JobID, j.Behavior)
		}
	}
	if m.MaturationH <= 0 {
		return fmt.Errorf("exp-run: maturation_hours must be positive")
	}
	return nil
}

// contentHash is sha256 over canonical JSON (Go maps marshal with sorted
// keys; the round-trip through canonicalJSON fixes whitespace only).
func (m *Manifest) contentHash() (string, error) {
	clone := *m
	clone.WorkloadVersion = ""
	b, err := json.Marshal(struct {
		ExperimentID  string            `json:"experiment_id"`
		WorkloadName  string            `json:"workload_name"`
		Seed          uint64            `json:"seed"`
		MaturationH   float64           `json:"maturation_hours"`
		Treatments    []TreatmentConfig `json:"treatments"`
		Jobs          []ManifestJob     `json:"jobs"`
		CharterDigest string            `json:"charter_digest,omitempty"`
		Synthetic     bool              `json:"synthetic"`
	}{
		ExperimentID:  clone.ExperimentID,
		WorkloadName:  clone.WorkloadName,
		Seed:          clone.Seed,
		MaturationH:   clone.MaturationH,
		Treatments:    clone.Treatments,
		Jobs:          canonJobs(clone.Jobs),
		CharterDigest: clone.CharterDigest,
		Synthetic:     clone.Synthetic,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonicalJSON(b))
	return fmt.Sprintf("%x", sum[:16]), nil
}

// canonJobs reorders each job's arm map deterministically (Go maps marshal
// with sorted keys already, but belt-and-braces for cross-implementation
// stability); it also sorts nothing else, preserving manifest job order.
func canonJobs(jobs []ManifestJob) []ManifestJob {
	out := make([]ManifestJob, len(jobs))
	for i, j := range jobs {
		keys := make([]string, 0, len(j.Arms))
		for k := range j.Arms {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		ordered := make(map[string]ArmTruth, len(j.Arms))
		for _, k := range keys {
			ordered[k] = j.Arms[k]
		}
		j.Arms = ordered
		out[i] = j
	}
	return out
}

// canonicalJSON compacts JSON to a canonical byte form.
func canonicalJSON(b []byte) []byte {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	cb, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return cb
}

// EligibleTreatments returns the job's eligible set (all when empty).
func (m *Manifest) EligibleTreatments(job ManifestJob, all []string) []string {
	if len(job.Eligible) == 0 {
		return all
	}
	return job.Eligible
}

// SimClock maps manifest order to deterministic timestamps: job i is
// assigned at t0 + i*step. Wall clocks never enter ledgers.
func SimClock(t0 time.Time, step time.Duration, i int) time.Time {
	return t0.Add(time.Duration(i) * step).UTC()
}
