// Package journalstore backs the gateway's storage interfaces with one
// shared transactional journal (go/assay/journal) for the experimental T3
// treatment. Decision, outcome, and safety rows commit through the same
// handle, so the W3/W4/W6 partial-commit states of the file ledgers are
// unrepresentable. Learner, cost book, monitor, controller, selection,
// settlement, and evidence paths run UNCHANGED against these interfaces.
package journalstore

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/journal"
	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Backend owns one journal handle and builds the three stores over it.
// Sharing the handle IS the transaction coordination: there is no
// additional coordinator code because every atomic unit already commits
// inside a single journal transaction.
//
// Backend also owns the replay cache: one in-memory projection of the
// journal shared by all three stores. Reads sync the tail (EventsSince
// the cached max seq, usually zero rows) and serve from memory, so
// steady-state serve/settle and boot recovery never pay a full-table
// scan per call. Writes go through the journal first, then sync the
// tail, so the cache always reflects committed rows (including the
// writer's own). No semantics change: the cache is a pure projection,
// invalid inputs are still refused by the journal, and ordering is
// always commit (seq) order.
type Backend struct {
	journal *journal.Journal
	dir     string
	file    string // full journal path (dir/name), for permission tightening

	mu        sync.Mutex
	maxSeq    uint64
	decisions []gateway.CommittedDecision
	decByID   map[string]int
	outcomes  []outcome.OutcomeEvent
	outLatest map[string]int // jobID -> index of highest version
	safety    []gateway.SafetyEvent
	execs     map[string][]gateway.DecisionExecution

	dec *JournalDecisionStore
	out *JournalOutcomeStore
	saf *JournalSafetyStore
}

// OpenBackend opens (creating) the treatment journal. The path doubles as
// the storage identity: a treatment directory never mixes journal and
// JSONL authorities (enforced by the runner wiring, asserted here by
// refusing to open where a decisions.jsonl already exists).
func OpenBackend(dir, name string) (*Backend, error) {
	for _, legacy := range []string{"decisions.jsonl", "outcomes.jsonl", "safety.jsonl"} {
		if _, err := statFile(dir + "/" + legacy); err == nil {
			return nil, fmt.Errorf("journalstore: %s already holds JSONL ledgers (refusing mixed authorities; start a new experiment)", dir)
		}
	}
	j, err := journal.Open(dir + "/" + name)
	if err != nil {
		return nil, err
	}
	b := &Backend{
		journal: j, dir: dir, file: dir + "/" + name,
		decByID: map[string]int{}, outLatest: map[string]int{},
		execs: map[string][]gateway.DecisionExecution{},
	}
	b.dec = &JournalDecisionStore{be: b}
	b.out = &JournalOutcomeStore{be: b}
	b.saf = &JournalSafetyStore{be: b}
	// SQLite creates the database (and WAL sidecars) under the process
	// umask — typically 0644. JSONL ledgers are 0600; match that posture
	// here, pilot-local, without touching the shared assay package.
	tightenJournalFiles(dir + "/" + name)
	return b, nil
}

// tightenJournalFiles best-effort chmods the journal file set to 0600.
// Missing sidecars (-wal/-shm when idle) are skipped, never errors.
func tightenJournalFiles(base string) {
	for _, p := range []string{base, base + "-wal", base + "-shm"} {
		if _, err := statFile(p); err != nil {
			continue
		}
		_ = chmodFile(p, 0o600)
	}
}

// Journal returns the shared handle (recovery paths, reporting loaders).
func (b *Backend) Journal() *journal.Journal { return b.journal }

// Close closes the shared handle.
func (b *Backend) Close() error {
	err := b.journal.Close()
	// WAL sidecars may be (re)created during writes after open-time
	// tightening; re-tighten at rest so the file set stays 0600.
	tightenJournalFiles(b.file)
	return err
}

// Decisions builds the decision store over the shared handle. The
// instance is a singleton: all callers share the backend replay cache.
func (b *Backend) Decisions() *JournalDecisionStore {
	return b.dec
}

// Outcomes builds the outcome store over the shared handle (singleton,
// shares the replay cache).
func (b *Backend) Outcomes() *JournalOutcomeStore {
	return b.out
}

// Safety builds the safety-event sink over the shared handle (singleton,
// shares the replay cache).
func (b *Backend) Safety() *JournalSafetyStore {
	return b.saf
}

// syncLocked pulls committed rows after the cached max seq into the
// projection. Caller holds b.mu. New rows are append-only, so the cache
// only grows; decByID/outLatest are maintained incrementally.
func (b *Backend) syncLocked() error {
	evs, err := b.journal.EventsSince(b.maxSeq)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if e.Seq > b.maxSeq {
			b.maxSeq = e.Seq
		}
		switch e.Kind {
		case "decision":
			d, err := fromJournalDecision(e)
			if err != nil {
				return err
			}
			if i, ok := b.decByID[d.DecisionID]; ok {
				b.decisions[i] = d
			} else {
				b.decByID[d.DecisionID] = len(b.decisions)
				b.decisions = append(b.decisions, d)
			}
		case "outcome":
			ev, err := fromJournalOutcome(e)
			if err != nil {
				return err
			}
			b.outcomes = append(b.outcomes, ev)
			if i, ok := b.outLatest[ev.JobID]; !ok || b.outcomes[i].Version < ev.Version {
				b.outLatest[ev.JobID] = len(b.outcomes) - 1
			}
		case "safety":
			var st journal.SafetyTransition
			if err := json.Unmarshal([]byte(e.Payload), &st); err != nil {
				return err
			}
			b.safety = append(b.safety, gateway.SafetyEvent{
				Seq: e.Seq, At: timeRFC3339(e.CreatedNS), Actor: st.Actor,
				Type: st.Type, Arm: st.Arm, Reason: st.Reason,
				Evidence: st.Evidence, ConfigHash: e.ConfigDigest,
			})
		}
	}
	return nil
}

// JournalDecisionStore implements gateway.DecisionStore (+ DecisionScanner)
// over journal decision rows. Seq maps to the journal rowid. All reads
// serve from the backend replay cache; see Backend.
type JournalDecisionStore struct {
	be *Backend
}

func toJournalDecision(d gateway.CommittedDecision) journal.Decision {
	jd := journal.Decision{
		DecisionID: d.DecisionID, JobID: d.JobID, StrategyID: d.StrategyID,
		SelectedArm: d.SelectedArmID, Eligible: d.EligibleArmIDs,
		Scores: d.SampledScores, PolicyID: d.LoggingPolicyID,
		ConfigHash: d.ConfigHash, ScoreKind: string(d.ScoreKind),
	}
	for _, s := range d.EligibleArmState {
		jd.EligibleState = append(jd.EligibleState, journal.ArmState{
			ArmID: s.ArmID, Alpha: s.Alpha, Beta: s.Beta, Pulls: s.Pulls,
		})
	}
	if d.CostAware != nil {
		jd.RuleVersion = d.CostAware.RuleVersion
		jd.Objective = d.CostAware.Objective
		jd.Fallback = d.CostAware.Fallback
		jd.CostPerSuc = d.CostAware.CostPerSuc
		// Budget recovery counts fallback picks (same rule as the file
		// ledger scan): Explore mirrors the flagged fallback bit so both
		// backends recover identical budgets.
		jd.Explore = d.CostAware.Fallback
	}
	return jd
}

func fromJournalDecision(e journal.StoredEvent) (gateway.CommittedDecision, error) {
	var jd journal.Decision
	if err := json.Unmarshal([]byte(e.Payload), &jd); err != nil {
		return gateway.CommittedDecision{}, err
	}
	d := gateway.CommittedDecision{
		DecisionID: jd.DecisionID, JobID: jd.JobID, StrategyID: jd.StrategyID,
		SelectedArmID: jd.SelectedArm, EligibleArmIDs: jd.Eligible,
		SampledScores: jd.Scores, ScoreKind: gateway.ScoreKind(jd.ScoreKind),
		LoggingPolicyID: jd.PolicyID, ConfigHash: jd.ConfigHash,
		Seq: e.Seq, OccurredAt: "",
	}
	for _, s := range jd.EligibleState {
		d.EligibleArmState = append(d.EligibleArmState, gateway.EligibleArmState{
			ArmID: s.ArmID, Alpha: s.Alpha, Beta: s.Beta, Pulls: s.Pulls,
		})
	}
	if jd.PolicyID == thompson.CostAwarePolicyID {
		d.CostAware = &thompson.CostAwareResult{
			ArmID: jd.SelectedArm, PolicyID: jd.PolicyID, Objective: jd.Objective,
			RuleVersion: jd.RuleVersion, Fallback: jd.Fallback, CostPerSuc: jd.CostPerSuc,
		}
	}
	return d, nil
}

// Commit validates, persists, and returns the stored record plus true when
// newly committed — mirroring FileDecisionStore semantics exactly,
// including OccurredAt preservation.
func (s *JournalDecisionStore) Commit(d gateway.CommittedDecision) (gateway.CommittedDecision, bool, error) {
	if err := d.Validate(); err != nil {
		return gateway.CommittedDecision{}, false, err
	}
	seq, committed, err := s.be.journal.CommitDecision(toJournalDecision(d), "")
	if err != nil {
		return gateway.CommittedDecision{}, false, err
	}
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if err := s.be.syncLocked(); err != nil {
		return gateway.CommittedDecision{}, false, err
	}
	if !committed {
		stored, ok := s.lookupLocked(d.DecisionID)
		if !ok {
			return gateway.CommittedDecision{}, false, fmt.Errorf("journalstore: duplicate vanished")
		}
		return stored, false, nil
	}
	d.Seq = seq
	return d, true, nil
}

// Lookup returns the committed decision, if any (index hit after a
// usually-empty tail sync).
func (s *JournalDecisionStore) Lookup(decisionID string) (gateway.CommittedDecision, bool) {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if err := s.be.syncLocked(); err != nil {
		return gateway.CommittedDecision{}, false
	}
	return s.lookupLocked(decisionID)
}

func (s *JournalDecisionStore) lookupLocked(decisionID string) (gateway.CommittedDecision, bool) {
	i, ok := s.be.decByID[decisionID]
	if !ok {
		return gateway.CommittedDecision{}, false
	}
	return s.withExecutions(s.be.decisions[i]), true
}

// MarkExecution records an execution-progress marker. Markers live in
// memory (guarded by the backend mutex); the decision must be committed
// first. Unchanged semantics: memory-only, like before.
func (s *JournalDecisionStore) MarkExecution(e gateway.DecisionExecution) error {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	s.be.execs[e.DecisionID] = append(s.be.execs[e.DecisionID], e)
	return nil
}

// Execution returns the latest marker for a decision, if any.
func (s *JournalDecisionStore) Execution(id string) (gateway.DecisionExecution, bool) {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	ms := s.be.execs[id]
	if len(ms) == 0 {
		return gateway.DecisionExecution{}, false
	}
	return ms[len(ms)-1], true
}

// Len counts committed decisions.
func (s *JournalDecisionStore) Len() int {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if err := s.be.syncLocked(); err != nil {
		return 0
	}
	return len(s.be.decisions)
}

// Scan replays committed decisions in sequence order (budget recovery).
// The snapshot is taken under lock; fn runs without it.
func (s *JournalDecisionStore) Scan(fn func(gateway.CommittedDecision) bool) error {
	s.be.mu.Lock()
	if err := s.be.syncLocked(); err != nil {
		s.be.mu.Unlock()
		return err
	}
	snap := make([]gateway.CommittedDecision, len(s.be.decisions))
	copy(snap, s.be.decisions)
	s.be.mu.Unlock()
	for _, d := range snap {
		if !fn(d) {
			break
		}
	}
	return nil
}

func (s *JournalDecisionStore) withExecutions(d gateway.CommittedDecision) gateway.CommittedDecision {
	return d
}

// JournalOutcomeStore implements outcome.OutcomeStore over journal outcome
// rows with full event fidelity (all attempt fields round-trip). All
// reads serve from the backend replay cache; see Backend.
type JournalOutcomeStore struct {
	be *Backend
}

func toJournalOutcome(ev outcome.OutcomeEvent) journal.SettledOutcome {
	o := journal.SettledOutcome{
		SchemaVersion: int(ev.SchemaVersion), EventType: string(ev.EventType),
		DecisionID: ev.DecisionID, JobID: ev.JobID,
		Version: ev.Version, Supersedes: ev.Supersedes,
		Status: string(ev.Status), DecidingAttempt: ev.DecidingAttemptID,
		HumanReviewCost: ev.HumanReviewCostUSD,
		VerifiedBy:      ev.VerifiedBy, VerifiedAt: ev.VerifiedAt,
		CorrectedAt: ev.CorrectedAt, OccurredAt: ev.OccurredAt,
	}
	for _, a := range ev.Attempts {
		ja := journal.OutcomeAttempt{
			AttemptID: a.AttemptID, ArmID: a.ArmID, CostUSD: a.CostUSD,
			Verified: string(a.Verified), ExecutorID: a.ExecutorID,
			Transport: string(a.Transport), LatencyMs: a.LatencyMs,
			Validation: string(a.Validation),
			VerifiedBy: a.VerifiedBy, VerifiedAt: a.VerifiedAt,
			FailureCategory: a.FailureCategory,
		}
		if a.InputTokens != nil {
			v := *a.InputTokens
			ja.InputTokens = &v
		}
		if a.OutputTokens != nil {
			v := *a.OutputTokens
			ja.OutputTokens = &v
		}
		for _, fc := range a.FieldCorrections {
			ja.FieldCorrections = append(ja.FieldCorrections, journal.FieldCorrection{
				Field: fc.Field, Before: fc.Before, After: fc.After,
			})
		}
		o.Attempts = append(o.Attempts, ja)
	}
	return o
}

func fromJournalOutcome(e journal.StoredEvent) (outcome.OutcomeEvent, error) {
	var o journal.SettledOutcome
	if err := json.Unmarshal([]byte(e.Payload), &o); err != nil {
		return outcome.OutcomeEvent{}, err
	}
	ev := outcome.OutcomeEvent{
		SchemaVersion: o.SchemaVersion, EventType: o.EventType,
		DecisionID: o.DecisionID, JobID: o.JobID,
		Version: o.Version, Supersedes: o.Supersedes,
		Status: outcome.JobStatus(o.Status), DecidingAttemptID: o.DecidingAttempt,
		HumanReviewCostUSD: o.HumanReviewCost, VerifiedBy: o.VerifiedBy,
		VerifiedAt: o.VerifiedAt, CorrectedAt: o.CorrectedAt,
		OccurredAt: o.OccurredAt, Seq: e.Seq,
	}
	for i, a := range o.Attempts {
		oa := outcome.Attempt{
			AttemptID: a.AttemptID, Seq: uint(i), ExecutorID: a.ExecutorID, ArmID: a.ArmID,
			Transport: outcome.TransportStatus(a.Transport), LatencyMs: a.LatencyMs,
			CostUSD: a.CostUSD, Validation: outcome.ValidationVerdict(a.Validation),
			FailureCategory: a.FailureCategory, Verified: outcome.VerifiedOutcome(a.Verified),
			VerifiedBy: a.VerifiedBy, VerifiedAt: a.VerifiedAt,
		}
		if a.InputTokens != nil {
			v := *a.InputTokens
			oa.InputTokens = &v
		}
		if a.OutputTokens != nil {
			v := *a.OutputTokens
			oa.OutputTokens = &v
		}
		for _, fc := range a.FieldCorrections {
			oa.FieldCorrections = append(oa.FieldCorrections, outcome.FieldCorrection{
				Field: fc.Field, Before: fc.Before, After: fc.After,
			})
		}
		ev.Attempts = append(ev.Attempts, oa)
	}
	return ev, nil
}

// Submit validates and commits; duplicates are idempotent, stale versions
// are refused (matching FileOutcomeStore: stale is an error, not silent).
func (s *JournalOutcomeStore) Submit(ev outcome.OutcomeEvent) (bool, error) {
	if err := ev.Validate(); err != nil {
		return false, err
	}
	applied, err := s.be.journal.SettleOutcome(toJournalOutcome(ev), "")
	if err != nil {
		return false, err
	}
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if err := s.be.syncLocked(); err != nil {
		return false, err
	}
	if applied {
		return true, nil
	}
	// Not applied: duplicate of latest (idempotent) or stale (refused).
	// Distinguish by latest version, exactly like the file store.
	latest, ok, err := s.be.journal.LatestVersion(ev.JobID)
	if err != nil {
		return false, err
	}
	if ok && latest == ev.Version {
		return false, nil
	}
	return false, fmt.Errorf("journalstore: stale version %d for job %q (latest %d)", ev.Version, ev.JobID, latest)
}

// Latest returns the highest committed version for a job (index hit
// after a usually-empty tail sync).
func (s *JournalOutcomeStore) Latest(jobID string) (outcome.OutcomeEvent, bool) {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if err := s.be.syncLocked(); err != nil {
		return outcome.OutcomeEvent{}, false
	}
	i, ok := s.be.outLatest[jobID]
	if !ok {
		return outcome.OutcomeEvent{}, false
	}
	return s.be.outcomes[i], true
}

// Events returns all committed events in sequence order (snapshot copy).
func (s *JournalOutcomeStore) Events() []outcome.OutcomeEvent {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if err := s.be.syncLocked(); err != nil {
		return nil
	}
	out := make([]outcome.OutcomeEvent, len(s.be.outcomes))
	copy(out, s.be.outcomes)
	return out
}

// Len counts committed outcome rows (all versions, like the file store).
func (s *JournalOutcomeStore) Len() int {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if err := s.be.syncLocked(); err != nil {
		return 0
	}
	return len(s.be.outcomes)
}

// JournalSafetyStore implements gateway.SafetyEventSink over journal
// safety rows. Seq maps to the journal rowid; timestamps come from the
// row's commit time. Close is a no-op: the Backend owns the shared handle.
// Reads serve from the backend replay cache; see Backend.
type JournalSafetyStore struct {
	be     *Backend
	mu     sync.Mutex
	failed bool
}

// Append persists one safety transition before its effect is visible.
func (s *JournalSafetyStore) Append(ev gateway.SafetyEvent) error {
	_, ok, err := s.be.journal.RecordSafety(journal.SafetyTransition{
		Actor: ev.Actor, Type: ev.Type, Arm: ev.Arm,
		Reason: ev.Reason, Evidence: ev.Evidence,
	}, ev.ConfigHash)
	if err != nil {
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
		return err
	}
	if !ok {
		return fmt.Errorf("journalstore: safety event not applied")
	}
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	return s.be.syncLocked()
}

// Events replays safety rows in sequence order (snapshot copy).
func (s *JournalSafetyStore) Events() ([]gateway.SafetyEvent, error) {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if err := s.be.syncLocked(); err != nil {
		return nil, err
	}
	out := make([]gateway.SafetyEvent, len(s.be.safety))
	copy(out, s.be.safety)
	return out, nil
}

// Failed reports the latched failure state.
func (s *JournalSafetyStore) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Close is a no-op (shared handle owned by Backend).
func (s *JournalSafetyStore) Close() error { return nil }

func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

func chmodFile(path string, mode os.FileMode) error { return os.Chmod(path, mode) }

func timeRFC3339(ns int64) string {
	return time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
}
