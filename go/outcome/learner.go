package outcome

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// RewardMapper converts a settled job outcome into the scalar reward folded
// into an arm posterior. It never changes the bandit update math: the reward
// flows through the existing Policy.Record, and the sampler is untouched.
type RewardMapper interface {
	// MapReward returns the reward and whether the event should move the
	// policy at all. UNKNOWN/PENDING must report learn=false.
	MapReward(ev OutcomeEvent) (reward float64, learn bool)
}

// BinaryStatusMapper is the default strategy-level mapping: ACCEPTED folds
// 1.0, REJECTED folds 0.0, anything else folds nothing. Cost, latency and
// quality shape the offline pilot metric (cost per verified successful job),
// not the bandit scalar; a cost-aware mapper can implement RewardMapper
// without touching this package.
type BinaryStatusMapper struct{}

func (BinaryStatusMapper) MapReward(ev OutcomeEvent) (float64, bool) {
	switch ev.Status {
	case StatusAccepted:
		return 1.0, true
	case StatusRejected:
		return 0.0, true
	default:
		return 0, false
	}
}

// NoopMapper never moves the policy. Static treatments (T0/T1) run the full
// verified decision + settlement machinery for accounting while learning
// nothing: their behavior stays fixed by construction. Additive only; the
// binary mapping and the update math are untouched.
type NoopMapper struct{}

func (NoopMapper) MapReward(ev OutcomeEvent) (float64, bool) { return 0, false }

// rngFor derives a deterministic RNG stream per (job, version). Bernoulli
// updates consume randomness, so replaying the same event must flip the same
// coin: seeding from the event identity (not from a shared stream position)
// makes every update reproducible regardless of application order.
func rngFor(jobID string, version uint64) *rand.Rand {
	h := sha256.New()
	h.Write([]byte("outcome-v1\x00"))
	h.Write([]byte(jobID))
	h.Write([]byte{0})
	var vb [8]byte
	binary.LittleEndian.PutUint64(vb[:], version)
	h.Write(vb[:])
	sum := h.Sum(nil)
	return rand.New(rand.NewPCG(
		binary.LittleEndian.Uint64(sum[:8]),
		binary.LittleEndian.Uint64(sum[8:16]),
	))
}

// Learner folds settled job outcomes into a thompson.Policy exactly once per
// (job, version), with latest-version-wins correction semantics:
//
//   - First-time versions apply incrementally.
//   - A newer version for an already-applied job triggers a rebuild from the
//     genesis snapshot over the full history, folding only the latest version
//     per job in ledger order. Beta updates are not invertible in general
//     (Bernoulli is stochastic, discounting touches every arm), so the old
//     contribution is removed by re-folding, never by subtraction.
//   - UNKNOWN/PENDING versions and versions whose deciding attempt maps to no
//     arm advance the applied cursor without touching any posterior.
//
// The arm set is fixed at genesis: events naming an unknown arm fail loudly
// instead of learning into the wrong place.
type Learner struct {
	mu      sync.Mutex
	policy  *thompson.Policy
	genesis thompson.Snapshot
	mapper  RewardMapper
	history func() []OutcomeEvent
	applied map[string]uint64
}

// NewLearner creates a learner over policy, snapshotting it as the rebuild
// genesis. History supplies the full committed event list for rebuilds
// (typically store.Events).
func NewLearner(policy *thompson.Policy, mapper RewardMapper, history func() []OutcomeEvent) *Learner {
	if mapper == nil {
		mapper = BinaryStatusMapper{}
	}
	if history == nil {
		history = func() []OutcomeEvent { return nil }
	}
	return &Learner{
		policy:  policy,
		genesis: policy.Snapshot(),
		mapper:  mapper,
		history: history,
		applied: make(map[string]uint64),
	}
}

// Policy returns the underlying policy. Callers must not Record into it
// directly while the learner owns it.
func (l *Learner) Policy() *thompson.Policy { return l.policy }

// Rebase adopts the policy's current learned state (including any added
// arms) as the new rebuild genesis. Arm-set changes under a live learner
// never happen silently: without Rebase, a rebuild restores the genesis
// arm set and folds fail loudly instead of learning into the wrong place.
func (l *Learner) Rebase() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.genesis = l.policy.Snapshot()
}

// armIDs extracts the arm ID set of a snapshot for arm-set comparison.
func armIDs(s thompson.Snapshot) map[string]bool {
	out := make(map[string]bool, len(s.Arms))
	for _, a := range s.Arms {
		out[a.ID] = true
	}
	return out
}

// Applied returns a copy of the (job -> applied version) cursor.
func (l *Learner) Applied() map[string]uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]uint64, len(l.applied))
	for k, v := range l.applied {
		out[k] = v
	}
	return out
}

// RestoreCursor replaces the applied cursor (crash-recovery path).
func (l *Learner) RestoreCursor(applied map[string]uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applied = make(map[string]uint64, len(applied))
	for k, v := range applied {
		l.applied[k] = v
	}
}

// Apply folds one event. It returns learned=true when an arm posterior moved.
// Duplicates (version <= applied) are idempotent no-ops returning false.
// A newer version for a known job rebuilds from genesis over the full
// history. A version gap (ev.Version > applied+1) is an error: the caller is
// missing history and must Rebuild once it has it.
//
// Concurrency: calls serialize on the learner mutex, but arrival order may
// differ from ledger order under concurrency. With a discount policy the
// result is order-dependent, so concurrent Apply is only equivalent to
// replay when arrivals match ledger order. The production settlement path
// serializes calls (gateway settleMu), preserving ledger order.
func (l *Learner) Apply(ev OutcomeEvent) (bool, error) {
	if err := ev.Validate(); err != nil {
		return false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, known := l.applied[ev.JobID]
	switch {
	case known && ev.Version <= cur:
		return false, nil // duplicate or stale redelivery: idempotent
	case known && ev.Version > cur+1:
		return false, fmt.Errorf("outcome: version gap for job %q: applied %d, got %d", ev.JobID, cur, ev.Version)
	case known:
		// Correction: re-fold from genesis so the superseded version's
		// contribution is removed, not added to.
		return l.rebuildLocked(append(l.history(), ev))
	default:
		if ev.Version != 1 {
			return false, fmt.Errorf("outcome: first version for job %q must be 1, got %d", ev.JobID, ev.Version)
		}
		return l.applyIncrementalLocked(ev)
	}
}

// Rebuild restores the genesis snapshot and folds the latest version per job
// from evs in ledger (Seq) order. It returns true when any posterior moved.
func (l *Learner) Rebuild(evs []OutcomeEvent) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rebuildLocked(evs)
}

func (l *Learner) rebuildLocked(evs []OutcomeEvent) (bool, error) {
	// Arm-set guard: a rebuild restores the genesis arm set. If the live
	// policy gained or lost arms since genesis (e.g. AddArm under a live
	// learner), restoring would silently wipe them and subsequent folds
	// would fail midway. Refuse loudly instead; Rebase() adopts changes.
	current := armIDs(l.policy.Snapshot())
	genesis := armIDs(l.genesis)
	if len(current) != len(genesis) {
		return false, fmt.Errorf("outcome: arm set changed under learner (live %d arms, genesis %d): call Rebase() to adopt", len(current), len(genesis))
	}
	for id := range current {
		if !genesis[id] {
			return false, fmt.Errorf("outcome: arm %q not in learner genesis: call Rebase() to adopt", id)
		}
	}
	// Behavior-policy tripwire (B5): the only config mutation path is
	// RestoreSnapshot below. If it ever changes the logging identity, the
	// rebuild aborts instead of learning under a swapped configuration.
	identityBefore := l.policy.LoggingPolicyID()
	// Rollback state: a fold failure midway must not leave a half-folded
	// policy behind with a reset cursor. Snapshot first; restore on error.
	savedPolicy := l.policy.Snapshot()
	savedApplied := make(map[string]uint64, len(l.applied))
	for k, v := range l.applied {
		savedApplied[k] = v
	}
	rollback := func(err error) (bool, error) {
		restoreErr := l.policy.RestoreSnapshot(savedPolicy)
		if restoreErr != nil {
			return false, fmt.Errorf("outcome: rebuild failed (%v) and rollback failed (%v)", err, restoreErr)
		}
		l.applied = savedApplied
		return false, err
	}
	if err := l.policy.RestoreSnapshot(l.genesis); err != nil {
		return false, fmt.Errorf("outcome: restore genesis: %w", err)
	}
	// Latest version per job; order jobs by first appearance (creation order).
	// Exact (job, version) duplicates collapse to the first occurrence, so a
	// redelivered history (or the corrected event echoed after history)
	// re-folds identically.
	latest := make(map[string]OutcomeEvent)
	firstSeq := make(map[string]uint64)
	seen := make(map[string]map[uint64]bool)
	for _, ev := range evs {
		if err := ev.Validate(); err != nil {
			return false, err
		}
		if seen[ev.JobID] == nil {
			seen[ev.JobID] = make(map[uint64]bool)
		}
		if seen[ev.JobID][ev.Version] {
			continue
		}
		seen[ev.JobID][ev.Version] = true
		if cur, ok := latest[ev.JobID]; !ok || ev.Version > cur.Version {
			latest[ev.JobID] = ev
		}
		if s, ok := firstSeq[ev.JobID]; !ok || ev.Seq < s {
			firstSeq[ev.JobID] = ev.Seq
		}
	}
	ordered := make([]OutcomeEvent, 0, len(latest))
	for _, ev := range latest {
		ordered = append(ordered, ev)
	}
	sort.Slice(ordered, func(i, j int) bool {
		si, sj := firstSeq[ordered[i].JobID], firstSeq[ordered[j].JobID]
		if si != sj {
			return si < sj
		}
		return ordered[i].JobID < ordered[j].JobID
	})
	moved := false
	l.applied = make(map[string]uint64, len(ordered))
	for _, ev := range ordered {
		m, err := l.foldLocked(l.policy, ev)
		if err != nil {
			return rollback(fmt.Errorf("outcome: rebuild fold job %q v%d: %w", ev.JobID, ev.Version, err))
		}
		moved = moved || m
		l.applied[ev.JobID] = ev.Version
	}
	if l.policy.LoggingPolicyID() != identityBefore {
		return rollback(fmt.Errorf("outcome: rebuild changed logging policy identity (was %q)", identityBefore))
	}
	return moved, nil
}

func (l *Learner) applyIncrementalLocked(ev OutcomeEvent) (bool, error) {
	moved, err := l.foldLocked(l.policy, ev)
	if err != nil {
		return false, err
	}
	l.applied[ev.JobID] = ev.Version
	return moved, nil
}

// foldLocked maps one event to at most one Record call against the deciding
// attempt's arm. Empty ArmID (e.g. human fallback) advances the cursor with
// no posterior movement: strategy-level accounting, not arm learning.
func (l *Learner) foldLocked(p *thompson.Policy, ev OutcomeEvent) (bool, error) {
	reward, learn := l.mapper.MapReward(ev)
	if !learn {
		return false, nil
	}
	var armID string
	for _, a := range ev.Attempts {
		if a.AttemptID == ev.DecidingAttemptID {
			armID = a.ArmID
			break
		}
	}
	if armID == "" {
		return false, nil
	}
	// Arm-set contract: the learner folds only into arms present at genesis.
	// Arms added later (AddArm under a live learner) are refused until an
	// explicit Rebase adopts them — otherwise a later rebuild would restore
	// the genesis arm set and either wipe them or fail midway.
	if !l.armInGenesisLocked(armID) {
		return false, fmt.Errorf("outcome: arm %q not in learner genesis: call Rebase() to adopt arm-set changes", armID)
	}
	if err := p.Record(rngFor(ev.JobID, ev.Version), armID, reward); err != nil {
		return false, fmt.Errorf("outcome: record job %q v%d: %w", ev.JobID, ev.Version, err)
	}
	return true, nil
}

// armInGenesisLocked reports whether an arm belongs to the genesis arm set.
func (l *Learner) armInGenesisLocked(armID string) bool {
	for _, a := range l.genesis.Arms {
		if a.ID == armID {
			return true
		}
	}
	return false
}

// CheckpointVersion is the checkpoint format version.
const CheckpointVersion = 1

// Checkpoint is a durable learning checkpoint: the policy snapshot, the
// applied cursor, the genesis snapshot for future rebuilds, and the count of
// ledger events already folded in.
type Checkpoint struct {
	Version   uint32            `json:"version"`
	Genesis   thompson.Snapshot `json:"genesis"`
	Policy    thompson.Snapshot `json:"policy"`
	Applied   map[string]uint64 `json:"applied"`
	LedgerLen int               `json:"ledger_len"`
}

// CheckpointOf captures the current learner state over a ledger of ledgerLen
// committed events.
func (l *Learner) CheckpointOf(ledgerLen int) Checkpoint {
	l.mu.Lock()
	defer l.mu.Unlock()
	applied := make(map[string]uint64, len(l.applied))
	for k, v := range l.applied {
		applied[k] = v
	}
	return Checkpoint{
		Version:   CheckpointVersion,
		Genesis:   l.genesis,
		Policy:    l.policy.Snapshot(),
		Applied:   applied,
		LedgerLen: ledgerLen,
	}
}

// SaveCheckpoint persists cp atomically (tmp + rename + fsync), mirroring
// thompson.FileStore durability.
func SaveCheckpoint(path string, cp Checkpoint) error {
	if cp.Version != CheckpointVersion {
		return fmt.Errorf("outcome: checkpoint version %d, want %d", cp.Version, CheckpointVersion)
	}
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("outcome: marshal checkpoint: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("outcome: mkdir checkpoint dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("outcome: open checkpoint tmp: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("outcome: write checkpoint: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("outcome: sync checkpoint: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("outcome: close checkpoint: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("outcome: rename checkpoint: %w", err)
	}
	// Directory sync: without it a crash can lose the rename itself,
	// leaving the previous checkpoint (or none) after reboot.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("outcome: open checkpoint dir: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("outcome: sync checkpoint dir: %w", err)
	}
	return nil
}

// LoadCheckpoint reads a checkpoint, returning (nil, nil) when absent.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("outcome: read checkpoint: %w", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, fmt.Errorf("outcome: decode checkpoint: %w", err)
	}
	if cp.Version != CheckpointVersion {
		return nil, fmt.Errorf("outcome: checkpoint version %d, want %d", cp.Version, CheckpointVersion)
	}
	return &cp, nil
}

// Settle commits one event and folds it into the learner in the crash-safe
// order: persist first, learn second. A crash between the two leaves the
// event on disk with no checkpoint behind it, which Resume replays exactly
// once via the cursor. Learning before persisting is the one order that can
// lose a contribution forever, so callers must use Settle (or Submit then
// Apply) and never Apply an uncommitted event.
func Settle(store OutcomeStore, learner *Learner, ev OutcomeEvent) (learned bool, err error) {
	applied, err := store.Submit(ev)
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil // duplicate delivery: already learned
	}
	return learner.Apply(ev)
}

// Resume rebuilds learner state after a restart: restore the checkpointed
// policy and cursor, then apply every ledger event beyond the checkpoint's
// high-watermark. Events already reflected in the checkpoint are skipped by
// the cursor, so cross-restart redelivery cannot learn twice.
func Resume(policy *thompson.Policy, events []OutcomeEvent, cp *Checkpoint, mapper RewardMapper, history func() []OutcomeEvent) (*Learner, error) {
	l := NewLearner(policy, mapper, history)
	if cp == nil {
		if _, err := l.Rebuild(events); err != nil {
			return nil, err
		}
		return l, nil
	}
	if err := policy.RestoreSnapshot(cp.Policy); err != nil {
		return nil, fmt.Errorf("outcome: restore checkpoint policy: %w", err)
	}
	l.genesis = cp.Genesis
	l.RestoreCursor(cp.Applied)
	for _, ev := range events {
		if int(ev.Seq) <= cp.LedgerLen {
			continue
		}
		if _, err := l.Apply(ev); err != nil {
			return nil, fmt.Errorf("outcome: replay after checkpoint: %w", err)
		}
	}
	return l, nil
}
