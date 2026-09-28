// Package reuse implements the verified-artifact-reuse research prototype
// (THOMPSON_VERIFIED_ARTIFACT_REUSE_ASSAY_V1, Phase 2).
//
// Research harness only: exact, evidence-bound reuse of verified execution
// results. No fuzzy matching, no embeddings, no approximate reuse, no remote
// cache, no production integration. Storage is a small standalone stdlib-only
// JSONL research store (no new dependencies, no new append-only authority:
// records mirror the transactional journal's facts and never replace them).
package reuse

import (
	"fmt"
	"sort"
)

// Validity is the reuse-validity of one stored artifact. It is disjoint from
// the outcome taxonomy (ACCEPTED/REJECTED/UNKNOWN/PENDING in go/outcome).
type Validity string

const (
	ValidityValid   Validity = "VALID"
	ValidityStale   Validity = "STALE"
	ValidityInvalid Validity = "INVALID"
	ValidityUnknown Validity = "UNKNOWN"
)

// Verification is the source-outcome status copied onto an artifact at publish.
type Verification string

const (
	VerificationAccepted Verification = "ACCEPTED"
	VerificationRejected Verification = "REJECTED"
	VerificationUnknown  Verification = "UNKNOWN"
)

// UpstreamPrefix marks a dependency edge on another artifact's digest, which
// is how transitive invalidation and correction propagation traverse the graph:
// a dep named "upstream:<key-digest>" carries the upstream artifact digest.
const UpstreamPrefix = "upstream:"

// Dep is one declared correctness dependency: a name and a content digest.
type Dep struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// ExecutionKey is the exact computational identity of reusable work (contract §1).
type ExecutionKey struct {
	Operation        string `json:"operation"`
	OperationVersion string `json:"operation_version"`
	InputDigest      string `json:"input_digest"`
	Deps             []Dep  `json:"deps"`
	Executor         string `json:"executor"`
	EnvDigest        string `json:"env_digest"`
	VerifierContract string `json:"verifier_contract"`
	PolicyDigest     string `json:"policy_digest"`
}

// SortedDeps returns a copy of the deps sorted by name (canonical order).
func (k ExecutionKey) SortedDeps() []Dep {
	out := append([]Dep(nil), k.Deps...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Validate enforces fail-closed construction: every correctness field must be
// present with a well-formed digest. Empty PolicyDigest is allowed only with
// an explicit justification recorded by the caller (workload doc); empty
// EnvDigest is allowed only when the workload declares no environment inputs.
func (k ExecutionKey) Validate() error {
	if k.Operation == "" || k.OperationVersion == "" {
		return fmt.Errorf("%w: operation and operation_version are required", ErrMissingDep)
	}
	if !looksLikeDigest(k.InputDigest) {
		return fmt.Errorf("%w: input_digest %q", ErrMissingDep, k.InputDigest)
	}
	if k.Executor == "" {
		return fmt.Errorf("%w: executor", ErrMissingDep)
	}
	if !looksLikeDigest(k.VerifierContract) {
		return fmt.Errorf("%w: verifier_contract %q", ErrMissingDep, k.VerifierContract)
	}
	seen := map[string]bool{}
	for _, d := range k.Deps {
		if d.Name == "" || !looksLikeDigest(d.Digest) {
			return fmt.Errorf("%w: dep %q digest %q", ErrMissingDep, d.Name, d.Digest)
		}
		if seen[d.Name] {
			return fmt.Errorf("reuse: duplicate dep name %q", d.Name)
		}
		seen[d.Name] = true
	}
	return nil
}

// Artifact is one persisted verified artifact (contract §2).
type Artifact struct {
	KeyDigest      string       `json:"key_digest"`
	KeyCanonical   string       `json:"key_canonical"`
	ArtifactDigest string       `json:"artifact_digest"`
	Verification   Verification `json:"verification"`
	EvidenceID     string       `json:"evidence_id"`
	VerifiedAt     string       `json:"verified_at"`
	ActualCostUSD  float64      `json:"actual_cost_usd"`
	Deps           []Dep        `json:"deps"`
	ReceiptID      string       `json:"receipt_id"`
	OutcomeJobID   string       `json:"outcome_job_id"`
	OutcomeVersion uint64       `json:"outcome_version"`
	State          Validity     `json:"state"`
	Reason         string       `json:"reason"`
	ChangedDep     string       `json:"changed_dep"`
}

// Outcome describes the current authoritative outcome for a job, as resolved
// through the caller's OutcomeLookup (journal replay in the assay).
type Outcome struct {
	JobID   string
	Version uint64
	Status  string // ACCEPTED, REJECTED, UNKNOWN, PENDING
	Found   bool
}

// OutcomeLookup resolves the current authoritative outcome version for a job.
type OutcomeLookup func(jobID string) Outcome
