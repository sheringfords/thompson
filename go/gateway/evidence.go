package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// EvidenceWriter is the durable ledger abstraction (V0 + Shadow V0 + Integrity V1).
// Append-only JSONL, one JSON object per line.
// Storage may be replaced later (e.g. object store) without changing router logic.
type EvidenceWriter interface {
	WriteDecisionStarted(e DecisionStarted) error
	WriteExecutionObserved(e ExecutionObserved) error
	WriteDecisionLearned(e DecisionLearned) error
	WriteShadowExecutionObserved(e ShadowExecutionObserved) error
	WriteShadowSkipped(e ShadowSkipped) error
}

// PosteriorSnapshot is a JSON-serializable copy of thompson.Posterior
// used in evidence. Kept flat for readability.
type PosteriorSnapshot struct {
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	Pulls uint64  `json:"pulls"`
}

func snapshotFrom(p thompson.Posterior) PosteriorSnapshot {
	return PosteriorSnapshot{Alpha: p.Alpha, Beta: p.Beta, Pulls: p.Pulls}
}

// EligibleArmState is the immutable snapshot of one eligible arm's posterior
// at decision time. Required for offline Thompson propensity reconstruction.
type EligibleArmState struct {
	ArmID string  `json:"arm_id"`
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	Pulls uint64  `json:"pulls"`
}

// DecisionStarted persisted BEFORE provider execution.
// Answers: what arms were available, why one was selected, what state preceded it.
// V0 fields preserved; shadow V0 adds external_request_id + shadow_*.
// V0 OPE adds eligible_arm_state for propensity reconstruction.
type DecisionStarted struct {
	SchemaVersion    int                `json:"schema_version"`
	EventType        string             `json:"event_type"` // "DecisionStarted"
	DecisionID       string             `json:"decision_id"` // canonical internal ID
	OccurredAt       string             `json:"occurred_at"` // RFC3339Nano
	EligibleArmIDs   []string           `json:"eligible_arm_ids"`
	SelectedArmID    string             `json:"selected_arm_id"`
	SampledScores    map[string]float64 `json:"sampled_scores"`
	PolicyConfigHash string             `json:"policy_config_hash"`
	PosteriorBefore  PosteriorSnapshot  `json:"posterior_before"`
	EligibleArmState []EligibleArmState `json:"eligible_arm_state,omitempty"`
	// Bandit log record fields for OPE
	LoggingPolicyID       string `json:"logging_policy_id,omitempty"`
	LoggingPolicyConfigHash string `json:"logging_policy_config_hash,omitempty"`
	// Job binding for verified settlement (additive; absent in pre-PR3A rows).
	JobID      string `json:"job_id,omitempty"`
	StrategyID string `json:"strategy_id,omitempty"`
	// Shadow V0 additions (omitempty for backward compat with V0 readers)
	ExternalRequestID *string `json:"external_request_id,omitempty"`
	ShadowEligible    bool    `json:"shadow_eligible"`
	ShadowSampled     bool    `json:"shadow_sampled"`
	ShadowArmID       *string `json:"shadow_arm_id,omitempty"`
}

// ExecutionObserved persisted AFTER provider execution, before learning.
// Token/cost fields are nullable pointers — nil encodes JSON null / absence.
// Never synthesize a constant cost when unavailable.
type ExecutionObserved struct {
	SchemaVersion int     `json:"schema_version"`
	EventType     string  `json:"event_type"` // "ExecutionObserved"
	DecisionID    string  `json:"decision_id"`
	OccurredAt    string  `json:"occurred_at"`
	ArmID         string  `json:"arm_id"`
	LatencyMs     float64 `json:"latency_ms"`
	Success       bool    `json:"success"`
	InputTokens   *int    `json:"input_tokens"`  // nil -> null
	OutputTokens  *int    `json:"output_tokens"` // nil -> null
	CostUSD       *float64 `json:"cost_usd"`     // nil -> null
}

// DecisionLearned persisted AFTER RecordOutcome.
// Shows reward and posterior transition.
type DecisionLearned struct {
	SchemaVersion    int               `json:"schema_version"`
	EventType        string            `json:"event_type"` // "DecisionLearned"
	DecisionID       string            `json:"decision_id"`
	OccurredAt       string            `json:"occurred_at"`
	ArmID            string            `json:"arm_id"`
	ComputedReward   float64           `json:"computed_reward"`
	PosteriorBefore  PosteriorSnapshot `json:"posterior_before"`
	PosteriorAfter   PosteriorSnapshot `json:"posterior_after"`
	TotalPullsAfter  uint64            `json:"total_pulls_after"`
}

// ShadowExecutionObserved is counterfactual evidence: outcome of a single
// non-selected arm on the same logical workload. Never mutates live policy.
type ShadowExecutionObserved struct {
	SchemaVersion int     `json:"schema_version"`
	EventType     string  `json:"event_type"` // "ShadowExecutionObserved"
	DecisionID    string  `json:"decision_id"` // joins to primary
	OccurredAt    string  `json:"occurred_at"`
	ArmID         string  `json:"arm_id"`
	PrimaryArmID  string  `json:"primary_arm_id"`
	LatencyMs     float64 `json:"latency_ms"`
	Success       bool    `json:"success"`
	InputTokens   *int    `json:"input_tokens"`
	OutputTokens  *int    `json:"output_tokens"`
	CostUSD       *float64 `json:"cost_usd"`
	ComputedReward float64 `json:"computed_reward"` // RewardPolicy reward, evidence only
	// V1 behavior-policy logging for offline evaluation
	PrimaryLoggingPolicyID       string  `json:"primary_logging_policy_id"`
	PrimaryLoggingPolicyConfigHash string `json:"primary_logging_policy_config_hash"`
	ShadowSelectionPolicyID      string  `json:"shadow_selection_policy_id"`
	ShadowSelectionProbability   float64 `json:"shadow_selection_probability"`
	EligibleArmCount             int     `json:"eligible_arm_count"`
	ShadowCandidateCount         int     `json:"shadow_candidate_count"`
}

// ShadowSkipped is emitted when a shadow was intended but suppressed for a
// machine-readable reason. It is the V1 fix for stale shadow_sampled evidence.
type ShadowSkipped struct {
	SchemaVersion          int     `json:"schema_version"`
	EventType              string  `json:"event_type"` // "ShadowSkipped"
	DecisionID             string  `json:"decision_id"`
	OccurredAt             string  `json:"occurred_at"`
	IntendedShadowArmID    *string `json:"intended_shadow_arm_id,omitempty"`
	Reason                 string  `json:"reason"` // enum below
	// V1 behavior-policy logging (when available)
	PrimaryLoggingPolicyID       string  `json:"primary_logging_policy_id,omitempty"`
	PrimaryLoggingPolicyConfigHash string `json:"primary_logging_policy_config_hash,omitempty"`
	ShadowSelectionPolicyID      string  `json:"shadow_selection_policy_id,omitempty"`
	ShadowSelectionProbability   *float64 `json:"shadow_selection_probability,omitempty"`
	EligibleArmCount             int     `json:"eligible_arm_count"`
	ShadowCandidateCount         int     `json:"shadow_candidate_count"`
}

// FileEvidenceWriter is a file-backed, append-only JSONL writer.
// Each event is one JSON line, serialized under a mutex so concurrent
// HTTP handlers cannot interleave fragments. File is opened with
// O_APPEND|O_CREATE|O_WRONLY and Sync() is called after each write.
//
// Durability (V0):
//   - Each Write does file.Write + file.Sync() -> data durable on local FS
//     after successful return.
//   - Not fsynced directory (rename not used; pure append, no tmp file).
//   - Single process, single file. No cross-replica coordination —
//     multiple replicas must use separate files.
//   - Crash after provider execution but before RecordOutcome:
//     DecisionStarted + ExecutionObserved durable; DecisionLearned missing;
//     posterior not updated — re-learning would require replay filtering by
//     missing DecisionLearned. Idempotency guard prevents re-apply if retried
//     with same decision_id in same process.
//   - Crash after RecordOutcome but before final append:
//     posterior already mutated (in-memory); DecisionLearned not durable.
//     On restart file lacks final line while in-memory state diverged.
//     V0 accepts single-replica semantics and does not claim global exactly-once.
//   - Writer does not truncate or rewrite; ledger is append-only.
//   - Write failures are returned, not silently discarded.
type FileEvidenceWriter struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// NewFileEvidenceWriter opens (or creates) the JSONL file at path for append.
// New files are created 0600: evidence can contain prompt-adjacent metadata and
// must not be world-readable. Existing files keep their current mode; tighten
// with `chmod 600` on upgrade. Caller should Close when done.
//
// Single writer is enforced with an exclusive, non-blocking flock held for
// the writer's lifetime: a second opener fails fast instead of interleaving
// JSONL fragments from two processes.
func NewFileEvidenceWriter(path string) (*FileEvidenceWriter, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("evidence: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("evidence: file %s is already held by another writer (single-writer enforced): %w", path, err)
	}
	return &FileEvidenceWriter{file: f, path: path}, nil
}

func (w *FileEvidenceWriter) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("evidence: marshal: %w", err)
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.file.Write(b); err != nil {
		return fmt.Errorf("evidence: write %s: %w", w.path, err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("evidence: sync %s: %w", w.path, err)
	}
	return nil
}

// WriteDecisionStarted implements EvidenceWriter.
func (w *FileEvidenceWriter) WriteDecisionStarted(e DecisionStarted) error {
	return w.writeJSON(e)
}

// WriteExecutionObserved implements EvidenceWriter.
func (w *FileEvidenceWriter) WriteExecutionObserved(e ExecutionObserved) error {
	return w.writeJSON(e)
}

// WriteDecisionLearned implements EvidenceWriter.
func (w *FileEvidenceWriter) WriteDecisionLearned(e DecisionLearned) error {
	return w.writeJSON(e)
}

// WriteShadowExecutionObserved implements EvidenceWriter.
func (w *FileEvidenceWriter) WriteShadowExecutionObserved(e ShadowExecutionObserved) error {
	return w.writeJSON(e)
}

// WriteShadowSkipped implements EvidenceWriter.
func (w *FileEvidenceWriter) WriteShadowSkipped(e ShadowSkipped) error {
	return w.writeJSON(e)
}

// Close flushes and closes the underlying file.
func (w *FileEvidenceWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	_ = syscall.Flock(int(w.file.Fd()), syscall.LOCK_UN)
	err := w.file.Close()
	w.file = nil
	return err
}

// MemoryEvidenceWriter is an in-memory writer for tests. It records events
// in slices under a mutex; JSON line integrity is preserved per event.
type MemoryEvidenceWriter struct {
	mu       sync.Mutex
	Started  []DecisionStarted
	Observed []ExecutionObserved
	Learned  []DecisionLearned
	Shadow   []ShadowExecutionObserved
	Skipped  []ShadowSkipped
	// FailNext causes the next write to return an injected error, to test
	// failure surfacing without touching the file system.
	FailNext error
	// Lines holds raw JSON lines in write order, for interleaving tests.
	Lines [][]byte
}

func (w *MemoryEvidenceWriter) WriteDecisionStarted(e DecisionStarted) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.FailNext != nil {
		err := w.FailNext
		w.FailNext = nil
		return err
	}
	w.Started = append(w.Started, e)
	b, _ := json.Marshal(e)
	w.Lines = append(w.Lines, append(b, '\n'))
	return nil
}
func (w *MemoryEvidenceWriter) WriteExecutionObserved(e ExecutionObserved) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.FailNext != nil {
		err := w.FailNext
		w.FailNext = nil
		return err
	}
	w.Observed = append(w.Observed, e)
	b, _ := json.Marshal(e)
	w.Lines = append(w.Lines, append(b, '\n'))
	return nil
}
func (w *MemoryEvidenceWriter) WriteDecisionLearned(e DecisionLearned) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.FailNext != nil {
		err := w.FailNext
		w.FailNext = nil
		return err
	}
	w.Learned = append(w.Learned, e)
	b, _ := json.Marshal(e)
	w.Lines = append(w.Lines, append(b, '\n'))
	return nil
}

func (w *MemoryEvidenceWriter) WriteShadowExecutionObserved(e ShadowExecutionObserved) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.FailNext != nil {
		err := w.FailNext
		w.FailNext = nil
		return err
	}
	w.Shadow = append(w.Shadow, e)
	b, _ := json.Marshal(e)
	w.Lines = append(w.Lines, append(b, '\n'))
	return nil
}

func (w *MemoryEvidenceWriter) WriteShadowSkipped(e ShadowSkipped) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.FailNext != nil {
		err := w.FailNext
		w.FailNext = nil
		return err
	}
	w.Skipped = append(w.Skipped, e)
	b, _ := json.Marshal(e)
	w.Lines = append(w.Lines, append(b, '\n'))
	return nil
}

// nowRFC3339Nano is test-sealed via var for determinism.
var nowFunc = time.Now

func nowRFC3339Nano() string { return nowFunc().UTC().Format(time.RFC3339Nano) }
