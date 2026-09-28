package reuse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store is a small standalone SQLite-free research store for verified
// artifacts: one JSONL file (fsync per commit batch), an in-memory index
// rebuilt deterministically on Open (crash/restart reconstruction), and a
// single-writer mutex. It mirrors journal facts; it is not an authority.
type Store struct {
	mu   sync.Mutex
	path string
	file *os.File
	// byKey: key_digest -> record. byArtifact: artifact_digest -> key_digests.
	byKey      map[string]*Artifact
	byArtifact map[string][]string
	// outcomes: authoritative correction view injected by the harness
	// (journal replay in production terms). Nil means "unknown source".
	outcomes map[string]Outcome
	commits  int
}

// Open creates or opens the store at path, rebuilding the index by replaying
// every record in file order (deterministic reconstruction).
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("reuse: mkdir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("reuse: open: %w", err)
	}
	s := &Store{
		path:       path,
		file:       f,
		byKey:      map[string]*Artifact{},
		byArtifact: map[string][]string{},
		outcomes:   map[string]Outcome{},
	}
	if err := s.replay(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) replay() error {
	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	sc := bufio.NewScanner(s.file)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var rec storedRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("reuse: corrupt record: %w", err)
		}
		s.applyRecord(rec)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reuse: replay: %w", err)
	}
	if _, err := s.file.Seek(0, 2); err != nil {
		return err
	}
	return nil
}

type storedRecord struct {
	Type     string   `json:"type"` // "publish" | "state"
	Artifact Artifact `json:"artifact"`
}

func (s *Store) applyRecord(rec storedRecord) {
	a := rec.Artifact
	cp := a
	s.byKey[a.KeyDigest] = &cp
	found := false
	for _, k := range s.byArtifact[a.ArtifactDigest] {
		if k == a.KeyDigest {
			found = true
		}
	}
	if !found {
		s.byArtifact[a.ArtifactDigest] = append(s.byArtifact[a.ArtifactDigest], a.KeyDigest)
	}
	s.commits++
}

func (s *Store) append(rec storedRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := s.file.Write(line); err != nil {
		return fmt.Errorf("reuse: write: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("reuse: sync: %w", err)
	}
	s.applyRecord(rec)
	return nil
}

// Close closes the backing file.
func (s *Store) Close() error { return s.file.Close() }

// Len returns the committed record count.
func (s *Store) Len() int { s.mu.Lock(); defer s.mu.Unlock(); return s.commits }

// SetOutcome injects the authoritative outcome view (test harness stand-in
// for journal replay): the current version and status for a job.
func (s *Store) SetOutcome(o Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes[o.JobID] = o
}

// Publish stores one verified artifact. Only ACCEPTED executions are
// publishable. Duplicate publication of a byte-identical record is idempotent.
// A different key or artifact digest under an existing key digest fails closed
// with ErrKeyConflict: two ExecutionKeys never alias silently to one record.
func (s *Store) Publish(key ExecutionKey, body PublishBody) (*Artifact, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if body.Verification != VerificationAccepted {
		return nil, fmt.Errorf("%w: got %s", ErrNotAccepted, body.Verification)
	}
	if !looksLikeDigest(body.ArtifactDigest) {
		return nil, fmt.Errorf("%w: artifact_digest %q", ErrMissingDep, body.ArtifactDigest)
	}
	if body.EvidenceID == "" || body.ReceiptID == "" || body.OutcomeJobID == "" {
		return nil, fmt.Errorf("%w: evidence, receipt and outcome job are required", ErrMissingDep)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kd := key.KeyDigest()
	if prev, ok := s.byKey[kd]; ok {
		if prev.KeyCanonical == string(key.Canonical()) &&
			prev.ArtifactDigest == body.ArtifactDigest &&
			prev.EvidenceID == body.EvidenceID &&
			prev.OutcomeJobID == body.OutcomeJobID &&
			prev.OutcomeVersion == body.OutcomeVersion {
			return prev, nil // idempotent retry
		}
		return nil, fmt.Errorf("%w: key digest %s already bound", ErrKeyConflict, kd[:16])
	}
	a := Artifact{
		KeyDigest:      kd,
		KeyCanonical:   string(key.Canonical()),
		ArtifactDigest: body.ArtifactDigest,
		Verification:   body.Verification,
		EvidenceID:     body.EvidenceID,
		VerifiedAt:     body.VerifiedAt,
		ActualCostUSD:  body.ActualCostUSD,
		Deps:           key.CurrentDeps(),
		ReceiptID:      body.ReceiptID,
		OutcomeJobID:   body.OutcomeJobID,
		OutcomeVersion: body.OutcomeVersion,
		State:          ValidityValid,
	}
	if err := s.append(storedRecord{Type: "publish", Artifact: a}); err != nil {
		return nil, err
	}
	return s.byKey[kd], nil
}

// PublishBody carries the result-side fields of publication.
type PublishBody struct {
	ArtifactDigest string
	Verification   Verification
	EvidenceID     string
	VerifiedAt     string
	ActualCostUSD  float64
	ReceiptID      string
	OutcomeJobID   string
	OutcomeVersion uint64
}

// Lookup returns the stored artifact for an exact key-digest match.
func (s *Store) Lookup(keyDigest string) (*Artifact, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byKey[keyDigest]
	return a, ok
}

// Decision is one auditable reuse decision.
type Decision struct {
	KeyDigest string   `json:"key_digest"`
	Validity  Validity `json:"validity"`
	Reason    string   `json:"reason"`
	Artifact  string   `json:"artifact_digest"`
	Hit       bool     `json:"hit"`
}

// Evaluate resolves the reuse validity of key against live dependency
// digests and the authoritative outcome view. live maps dep name -> current
// digest; nil means "replay the key's own claim" (exact-replay probe). A key
// hit whose live world differs on any declared dep is STALE; unresolvable
// required deps, unknown outcomes, or superseded sources are INVALID/UNKNOWN,
// never VALID.
func (s *Store) Evaluate(key ExecutionKey, live map[string]string, lookup OutcomeLookup) Decision {
	kd := key.KeyDigest()
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.byKey[kd]
	if !ok {
		return Decision{KeyDigest: kd, Validity: ValidityUnknown, Reason: "no artifact for key", Hit: false}
	}
	if stored.State == ValidityInvalid {
		return Decision{KeyDigest: kd, Validity: ValidityInvalid, Reason: stored.Reason, Artifact: stored.ArtifactDigest, Hit: true}
	}
	// 1. Dependency comparison: stored snapshot vs the live world.
	cur := map[string]string{}
	if live != nil {
		cur = live
	} else {
		for _, d := range key.CurrentDeps() {
			cur[d.Name] = d.Digest
		}
	}
	for _, d := range stored.Deps {
		c, ok := cur[d.Name]
		if !ok || c == "" || !looksLikeDigest(c) {
			return s.markLocked(stored, ValidityUnknown,
				fmt.Sprintf("required dependency %q unresolvable", d.Name), d.Name)
		}
		if c != d.Digest {
			dec := s.markLocked(stored, ValidityStale,
				fmt.Sprintf("dependency %q changed", d.Name), d.Name)
			s.staleDownstreamLocked(kd, d.Name)
			return dec
		}
	}
	// 2. Source verification must still be the authoritative ACCEPTED version.
	if lookup != nil {
		o := lookup(stored.OutcomeJobID)
		if !o.Found {
			return s.markLocked(stored, ValidityUnknown, "source outcome unknown", "")
		}
		if o.Version != stored.OutcomeVersion || o.Status != string(VerificationAccepted) {
			return s.markLocked(stored, ValidityInvalid,
				fmt.Sprintf("source outcome superseded: v%d/%s -> v%d/%s",
					stored.OutcomeVersion, stored.Verification, o.Version, o.Status), "")
		}
	} else if out, ok := s.outcomes[stored.OutcomeJobID]; ok {
		if out.Version != stored.OutcomeVersion || out.Status != string(VerificationAccepted) {
			return s.markLocked(stored, ValidityInvalid,
				fmt.Sprintf("source outcome superseded: v%d -> v%d/%s",
					stored.OutcomeVersion, out.Version, out.Status), "")
		}
	}
	if stored.State == ValidityStale {
		// Previously marked stale but everything matches now: the exact
		// ExecutionKey is again valid. Per contract §4 this alone does not
		// auto-revive; revival additionally requires the source outcome to
		// still be authoritative (checked above) — restore to VALID and audit.
		return s.markLocked(stored, ValidityValid, "exact key valid again; source still authoritative", "")
	}
	return Decision{KeyDigest: kd, Validity: ValidityValid, Reason: "exact key match; deps and source verified", Artifact: stored.ArtifactDigest, Hit: true}
}

func (s *Store) markLocked(a *Artifact, v Validity, reason, changed string) Decision {
	if a.State != v || a.Reason != reason {
		a.State = v
		a.Reason = reason
		a.ChangedDep = changed
		_ = s.append(storedRecord{Type: "state", Artifact: *a})
	}
	return Decision{KeyDigest: a.KeyDigest, Validity: v, Reason: reason, Artifact: a.ArtifactDigest, Hit: true}
}

// staleDownstreamLocked transitively stales dependents of an already-staled
// root (caller holds s.mu).
func (s *Store) staleDownstreamLocked(root, originDep string) {
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range s.dependentsLocked(cur) {
			if dep.State != ValidityValid {
				continue
			}
			dep.State = ValidityStale
			dep.Reason = "transitive: upstream " + cur[:16] + " stale (" + originDep + ")"
			dep.ChangedDep = UpstreamPrefix + cur
			_ = s.append(storedRecord{Type: "state", Artifact: *dep})
			queue = append(queue, dep.KeyDigest)
		}
	}
}

// dependentsLocked finds artifacts with an upstream edge on keyDigest.
func (s *Store) dependentsLocked(keyDigest string) []*Artifact {
	var out []*Artifact
	for _, a := range s.byKey {
		for _, d := range a.Deps {
			if d.Name == UpstreamPrefix+keyDigest {
				out = append(out, a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeyDigest < out[j].KeyDigest })
	return out
}

// PropagateCorrection marks every artifact sourced from jobID at a superseded
// version INVALID and transitively invalidates downstream dependents.
// Duplicate delivery (same version) and stale delivery (older version) are
// no-ops returning the count of newly invalidated records.
func (s *Store) PropagateCorrection(jobID string, newVersion uint64, newStatus string) (invalidated int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes[jobID] = Outcome{JobID: jobID, Version: newVersion, Status: newStatus, Found: true}
	queue := []string{}
	for _, a := range s.byKey {
		if a.OutcomeJobID == jobID && a.OutcomeVersion < newVersion && a.State != ValidityInvalid {
			reason := fmt.Sprintf("correction superseded source: v%d -> v%d/%s", a.OutcomeVersion, newVersion, newStatus)
			a.State = ValidityInvalid
			a.Reason = reason
			_ = s.append(storedRecord{Type: "state", Artifact: *a})
			invalidated++
			queue = append(queue, a.KeyDigest)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range s.dependentsLocked(cur) {
			if dep.State == ValidityInvalid {
				continue
			}
			dep.State = ValidityInvalid
			dep.Reason = "transitive: upstream " + cur[:16] + " invalidated"
			dep.ChangedDep = UpstreamPrefix + cur
			_ = s.append(storedRecord{Type: "state", Artifact: *dep})
			invalidated++
			queue = append(queue, dep.KeyDigest)
		}
	}
	return invalidated
}

// PropagateStale transitively marks dependents of keyDigest STALE (used when a
// dependency change or upstream invalidation fans out through the graph).
// Returns the newly-staled count, the fan-out latency inputs being measured by
// callers, and records reason + originating dependency on each record.
func (s *Store) PropagateStale(keyDigest, originDep, reason string) (staled int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.byKey[keyDigest]
	if !ok {
		return 0
	}
	if root.State == ValidityValid {
		root.State = ValidityStale
		root.Reason = reason
		root.ChangedDep = originDep
		_ = s.append(storedRecord{Type: "state", Artifact: *root})
		staled++
	}
	queue := []string{keyDigest}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range s.dependentsLocked(cur) {
			if dep.State != ValidityValid {
				continue
			}
			dep.State = ValidityStale
			dep.Reason = "transitive: upstream " + cur[:16] + " stale (" + originDep + ")"
			dep.ChangedDep = UpstreamPrefix + cur
			_ = s.append(storedRecord{Type: "state", Artifact: *dep})
			staled++
			queue = append(queue, dep.KeyDigest)
		}
	}
	return staled
}

// VerifyBytes re-hashes candidate bytes against the stored artifact digest.
func (s *Store) VerifyBytes(keyDigest string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byKey[keyDigest]
	if !ok {
		return fmt.Errorf("reuse: no artifact for key")
	}
	if DigestBytes(body) != a.ArtifactDigest {
		return fmt.Errorf("reuse: artifact bytes do not match bound digest")
	}
	return nil
}

// GraphStats reports record/edge counts and on-disk bytes for scaling analysis.
func (s *Store) GraphStats() (artifacts, edges int, bytesOnDisk int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.byKey {
		artifacts++
		edges += len(a.Deps)
	}
	if fi, err := os.Stat(s.path); err == nil {
		bytesOnDisk = fi.Size()
	}
	return artifacts, edges, bytesOnDisk
}
