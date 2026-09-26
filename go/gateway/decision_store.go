package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// scoreKindFor maps the active selection configuration plus the snapshot's
// forced flag to the recorded score kind (contract §1.1.1).
func scoreKindFor(kind thompson.SelectionKind, forced bool) ScoreKind {
	switch kind {
	case thompson.UCBRegularized:
		return ScoreSamplePlusBonus
	case thompson.PhasedSelection:
		if forced {
			return ScorePosteriorMeans
		}
		return ScoreBetaSamples
	default:
		return ScoreBetaSamples
	}
}

// ScoreKind names what DecisionSnapshot.Scores contains. Scores are
// observability only — action probabilities always come from reconstructing
// eligible_arm_state (contract §1.1.1, audit A2).
type ScoreKind string

const (
	// ScoreBetaSamples: one Thompson draw per arm decided the selection.
	ScoreBetaSamples ScoreKind = "beta_samples"
	// ScoreSamplePlusBonus: UCB composites decided.
	ScoreSamplePlusBonus ScoreKind = "sample_plus_bonus"
	// ScorePosteriorMeans: a forced (phased quota) or custom-strategy choice;
	// scores are means, not samples.
	ScorePosteriorMeans ScoreKind = "posterior_means"
)

// ExecutionPhase tracks how far a committed decision got. Committed means
// selected-but-not-executed; Dispatched means handed to a provider;
// Observed means an execution outcome was recorded; Unknown covers lost,
// cancelled, or otherwise unobserved executions.
type ExecutionPhase string

const (
	PhaseCommitted  ExecutionPhase = "committed"
	PhaseDispatched ExecutionPhase = "dispatched"
	PhaseObserved   ExecutionPhase = "observed"
	PhaseUnknown    ExecutionPhase = "unknown"
)

// CommittedDecision is the immutable identity + selection evidence of one
// live decision, persisted before any external execution begins.
type CommittedDecision struct {
	DecisionID       string             `json:"decision_id"`
	JobID            string             `json:"job_id"`
	StrategyID       string             `json:"strategy_id"`
	SelectedArmID    string             `json:"selected_arm_id"`
	EligibleArmIDs   []string           `json:"eligible_arm_ids"`
	EligibleArmState []EligibleArmState `json:"eligible_arm_state"`
	SampledScores    map[string]float64 `json:"sampled_scores"`
	ScoreKind        ScoreKind          `json:"score_kind"`
	LoggingPolicyID  string             `json:"logging_policy_id"`
	ConfigHash       string             `json:"config_hash"`
	// Seq is the ledger-side monotonic decision number, assigned on commit.
	// Readers use it for same-version assertions (contract policy_version).
	Seq        uint64 `json:"seq"`
	OccurredAt string `json:"occurred_at"`
}

// Validate checks structural invariants of a committed decision.
func (d CommittedDecision) Validate() error {
	if d.DecisionID == "" || d.JobID == "" || d.StrategyID == "" {
		return fmt.Errorf("decision: decision_id, job_id and strategy_id are required")
	}
	if len(d.EligibleArmIDs) == 0 {
		return fmt.Errorf("decision: no eligible arms")
	}
	in := false
	for _, id := range d.EligibleArmIDs {
		if id == d.SelectedArmID {
			in = true
			break
		}
	}
	if !in {
		return fmt.Errorf("decision: selected %q not in eligible set", d.SelectedArmID)
	}
	if len(d.EligibleArmState) != len(d.EligibleArmIDs) {
		return fmt.Errorf("decision: eligible_arm_state length mismatch")
	}
	stateByArm := make(map[string]bool, len(d.EligibleArmState))
	for _, s := range d.EligibleArmState {
		stateByArm[s.ArmID] = true
	}
	for _, id := range d.EligibleArmIDs {
		if !stateByArm[id] {
			return fmt.Errorf("decision: eligible arm %q missing from eligible_arm_state", id)
		}
	}
	if len(d.SampledScores) != len(d.EligibleArmIDs) {
		return fmt.Errorf("decision: sampled_scores length mismatch")
	}
	for _, id := range d.EligibleArmIDs {
		if _, ok := d.SampledScores[id]; !ok {
			return fmt.Errorf("decision: sampled_scores missing eligible arm %q", id)
		}
	}
	switch d.ScoreKind {
	case ScoreBetaSamples, ScoreSamplePlusBonus, ScorePosteriorMeans:
	default:
		return fmt.Errorf("decision: unknown score kind %q", d.ScoreKind)
	}
	if d.LoggingPolicyID == "" || d.ConfigHash == "" {
		return fmt.Errorf("decision: logging_policy_id and config_hash are required")
	}
	return nil
}

// DecisionExecution is an immutable execution-progress marker for a committed
// decision. Markers are append-only; the latest one per decision is current.
type DecisionExecution struct {
	DecisionID string         `json:"decision_id"`
	Phase      ExecutionPhase `json:"phase"`
	Transport  string         `json:"transport,omitempty"`
	LatencyMs  *float64       `json:"latency_ms,omitempty"`
	OccurredAt string         `json:"occurred_at"`
	// N is the per-decision marker number (1, 2, … in commit order for this
	// decision), assigned by the store. It is independent of the decision
	// ledger Seq: execution markers never share the decision version
	// namespace. Files written before per-decision numbering carry "seq"
	// instead, which recovery ignores while assigning N in encounter order.
	N uint64 `json:"n"`
}

func (e DecisionExecution) Validate() error {
	if e.DecisionID == "" {
		return fmt.Errorf("decision: execution marker missing decision_id")
	}
	switch e.Phase {
	case PhaseDispatched, PhaseObserved, PhaseUnknown:
	default:
		return fmt.Errorf("decision: unknown execution phase %q", e.Phase)
	}
	return nil
}

func decisionsEqual(a, b CommittedDecision) bool {
	ac, bc := a, b
	ac.Seq, bc.Seq = 0, 0
	ab, err := json.Marshal(ac)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(bc)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// DecisionStore is the durable registry of committed live decisions. Commit
// assigns Seq and persists before the caller may dispatch execution; Lookup
// recovers a decision after restart for settlement matching.
//
// Durability contract: implementations wired into verified mode MUST survive
// process restart (the file store does; custom in-memory implementations do
// not and are rejected at router construction). There is no read-repair:
// Lookup reflects exactly what Commit persisted.
type DecisionStore interface {
	// Commit validates, assigns Seq, persists, and returns the stored record
	// plus true when newly committed. An exact-ID exact-content retry returns
	// the existing record plus false. A conflicting reuse of an existing ID
	// is rejected with an error and persists nothing.
	Commit(d CommittedDecision) (stored CommittedDecision, committed bool, err error)
	// Lookup returns the committed decision, if any.
	Lookup(decisionID string) (CommittedDecision, bool)
	// MarkExecution appends an execution-progress marker. The decision must
	// be committed first.
	MarkExecution(e DecisionExecution) error
	// Execution returns the latest marker for a decision, if any.
	Execution(decisionID string) (DecisionExecution, bool)
	Len() int
}

// MemoryDecisionStore is an in-memory DecisionStore for tests.
type MemoryDecisionStore struct {
	mu         sync.Mutex
	decisions  []CommittedDecision
	byID       map[string]int
	executions map[string]DecisionExecution
}

func NewMemoryDecisionStore() *MemoryDecisionStore {
	return &MemoryDecisionStore{byID: make(map[string]int), executions: make(map[string]DecisionExecution)}
}

func (s *MemoryDecisionStore) Commit(d CommittedDecision) (CommittedDecision, bool, error) {
	if err := d.Validate(); err != nil {
		return CommittedDecision{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.byID[d.DecisionID]; ok {
		if decisionsEqual(s.decisions[idx], d) {
			return s.decisions[idx], false, nil
		}
		return CommittedDecision{}, false, fmt.Errorf("decision: conflicting reuse of id %q", d.DecisionID)
	}
	d.Seq = uint64(len(s.decisions) + 1)
	s.decisions = append(s.decisions, d)
	s.byID[d.DecisionID] = len(s.decisions) - 1
	return d, true, nil
}

func (s *MemoryDecisionStore) Lookup(id string) (CommittedDecision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.byID[id]
	if !ok {
		return CommittedDecision{}, false
	}
	return s.decisions[idx], true
}

func (s *MemoryDecisionStore) MarkExecution(e DecisionExecution) error {
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[e.DecisionID]; !ok {
		return fmt.Errorf("decision: cannot mark uncommitted decision %q", e.DecisionID)
	}
	if cur, ok := s.executions[e.DecisionID]; ok {
		e.N = cur.N + 1
	} else {
		e.N = 1
	}
	s.executions[e.DecisionID] = e
	return nil
}

func (s *MemoryDecisionStore) Execution(id string) (DecisionExecution, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.executions[id]
	return e, ok
}

func (s *MemoryDecisionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.decisions)
}

// decisionRecord is the on-disk envelope: one JSON object per line.
type decisionRecord struct {
	Type      string             `json:"type"` // "committed" | "execution"
	Committed *CommittedDecision `json:"committed,omitempty"`
	Execution *DecisionExecution `json:"execution,omitempty"`
}

// FileDecisionStore is a file-backed, append-only DecisionStore. Same
// durability contract as the outcome ledger: per-commit write+sync, 0600,
// exclusive non-blocking flock (single writer), torn-tail truncation on open.
type FileDecisionStore struct {
	mu         sync.Mutex
	file       *os.File
	path       string
	decisions  []CommittedDecision
	byID       map[string]int
	executions map[string]DecisionExecution
	tornTail   bool
}

func NewFileDecisionStore(path string) (*FileDecisionStore, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("decision: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("decision: store %s is already held by another writer (single-writer enforced): %w", path, err)
	}
	s := &FileDecisionStore{file: f, path: path, byID: make(map[string]int), executions: make(map[string]DecisionExecution)}
	if err := s.recover(); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

func (s *FileDecisionStore) appendRecord(rec decisionRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("decision: marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := s.file.Write(b); err != nil {
		return fmt.Errorf("decision: write %s: %w", s.path, err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("decision: sync %s: %w", s.path, err)
	}
	return nil
}

func (s *FileDecisionStore) recover() error {
	st, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("decision: stat %s: %w", s.path, err)
	}
	if st.Size() == 0 {
		return nil
	}
	complete := true
	if tail := make([]byte, 1); true {
		if _, err := s.file.ReadAt(tail, st.Size()-1); err != nil {
			return fmt.Errorf("decision: read tail %s: %w", s.path, err)
		}
		complete = tail[0] == '\n'
	}
	if _, err := s.file.Seek(0, 0); err != nil {
		return fmt.Errorf("decision: seek %s: %w", s.path, err)
	}
	if !complete {
		// The final line was never newline-terminated, so its Submit never
		// completed: drop it before indexing, even if it parses.
		data, err := io.ReadAll(s.file)
		if err != nil {
			return fmt.Errorf("decision: read %s: %w", s.path, err)
		}
		truncTo := int64(bytes.LastIndexByte(data, '\n') + 1)
		if err := s.file.Truncate(truncTo); err != nil {
			return fmt.Errorf("decision: truncate torn tail %s: %w", s.path, err)
		}
		if _, err := s.file.Seek(0, 0); err != nil {
			return fmt.Errorf("decision: seek %s: %w", s.path, err)
		}
		if err := s.file.Sync(); err != nil {
			return fmt.Errorf("decision: sync %s: %w", s.path, err)
		}
		s.tornTail = true
	}
	scanner := bufio.NewScanner(s.file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	var lineStart int64
	index := func(rec decisionRecord) error {
		switch rec.Type {
		case "committed":
			if rec.Committed == nil {
				return fmt.Errorf("decision: committed record missing payload")
			}
			ev := *rec.Committed
			if err := ev.Validate(); err != nil {
				return err
			}
			ev.Seq = uint64(len(s.decisions) + 1)
			if idx, ok := s.byID[ev.DecisionID]; ok {
				if !decisionsEqual(s.decisions[idx], ev) {
					return fmt.Errorf("decision: ledger reuses id %q with conflicting content", ev.DecisionID)
				}
				return nil
			}
			s.decisions = append(s.decisions, ev)
			s.byID[ev.DecisionID] = len(s.decisions) - 1
		case "execution":
			if rec.Execution == nil {
				return fmt.Errorf("decision: execution record missing payload")
			}
			e := *rec.Execution
			if err := e.Validate(); err != nil {
				return err
			}
			if _, ok := s.byID[e.DecisionID]; !ok {
				return fmt.Errorf("decision: execution marker for uncommitted %q", e.DecisionID)
			}
			if cur, ok := s.executions[e.DecisionID]; ok {
				e.N = cur.N + 1
			} else {
				e.N = 1
			}
			s.executions[e.DecisionID] = e
		default:
			return fmt.Errorf("decision: unknown record type %q", rec.Type)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			lineStart++
			continue
		}
		var rec decisionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// Every scanned line is newline-terminated here (the
			// unterminated tail was already truncated above), so a parse
			// failure is corruption, not a crash tear: fail loudly rather
			// than truncating valid history that follows.
			return fmt.Errorf("decision: ledger %s has corrupt line at ~offset %d: %w", s.path, lineStart, err)
		}
		if err := index(rec); err != nil {
			return fmt.Errorf("decision: ledger %s at ~offset %d: %w", s.path, lineStart, err)
		}
		lineStart += int64(len(line)) + 1
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("decision: ledger %s scan failed at ~offset %d: %w", s.path, lineStart, err)
	}
	// Only the unterminated tail (handled above) is ever truncated: every
	// line scanned here was newline-terminated, hence committed.
	if _, err := s.file.Seek(0, 2); err != nil {
		return fmt.Errorf("decision: seek end %s: %w", s.path, err)
	}
	return nil
}

func (s *FileDecisionStore) Commit(d CommittedDecision) (CommittedDecision, bool, error) {
	if err := d.Validate(); err != nil {
		return CommittedDecision{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.byID[d.DecisionID]; ok {
		if decisionsEqual(s.decisions[idx], d) {
			return s.decisions[idx], false, nil
		}
		return CommittedDecision{}, false, fmt.Errorf("decision: conflicting reuse of id %q", d.DecisionID)
	}
	d.Seq = uint64(len(s.decisions) + 1)
	if err := s.appendRecord(decisionRecord{Type: "committed", Committed: &d}); err != nil {
		return CommittedDecision{}, false, err
	}
	s.decisions = append(s.decisions, d)
	s.byID[d.DecisionID] = len(s.decisions) - 1
	return d, true, nil
}

func (s *FileDecisionStore) Lookup(id string) (CommittedDecision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok {
		return CommittedDecision{}, false
	}
	return s.decisions[d], true
}

func (s *FileDecisionStore) MarkExecution(e DecisionExecution) error {
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[e.DecisionID]; !ok {
		return fmt.Errorf("decision: cannot mark uncommitted decision %q", e.DecisionID)
	}
	if cur, ok := s.executions[e.DecisionID]; ok {
		e.N = cur.N + 1
	} else {
		e.N = 1
	}
	if err := s.appendRecord(decisionRecord{Type: "execution", Execution: &e}); err != nil {
		return err
	}
	s.executions[e.DecisionID] = e
	return nil
}

func (s *FileDecisionStore) Execution(id string) (DecisionExecution, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.executions[id]
	return e, ok
}

func (s *FileDecisionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.decisions)
}

// TornTail reports whether recovery truncated a torn trailing write.
func (s *FileDecisionStore) TornTail() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tornTail
}

// Close releases the single-writer lock and closes the store.
func (s *FileDecisionStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	_ = syscall.Flock(int(s.file.Fd()), syscall.LOCK_UN)
	err := s.file.Close()
	s.file = nil
	return err
}
