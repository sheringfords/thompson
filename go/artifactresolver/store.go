package artifactresolver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store is a durable split-identity record: artifacts keyed by
// ComputationKey digest, verification claims keyed by VerificationKey
// digest with full append-only history. One JSONL file, fsync per commit,
// in-memory indexes rebuilt deterministically on open.
type Store struct {
	mu         sync.Mutex
	path       string
	file       *os.File
	arts       map[string]*ArtifactRecord
	claims     map[string]*VerificationClaim
	byArtifact map[string][]string
	bodies     map[string][]byte
}

// Open creates or opens the store at path, replaying history
// deterministically. Directories are created with owner-only permissions,
// as is the file itself.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, file: f, arts: map[string]*ArtifactRecord{},
		claims: map[string]*VerificationClaim{}, byArtifact: map[string][]string{},
		bodies: map[string][]byte{}}
	if err := s.replay(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

type storedLine struct {
	Type  string             `json:"type"` // artifact | claim | revoke
	Art   *ArtifactRecord    `json:"art,omitempty"`
	Claim *VerificationClaim `json:"claim,omitempty"`
	Body  []byte             `json:"body,omitempty"`
}

func (s *Store) replay() error {
	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	sc := bufio.NewScanner(s.file)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec storedLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return fmt.Errorf("artifactresolver: corrupt line: %w", err)
		}
		switch rec.Type {
		case "artifact":
			cp := *rec.Art
			s.arts[cp.CompDigest] = &cp
			if len(rec.Body) > 0 {
				s.bodies[cp.ArtifactDigest] = append([]byte(nil), rec.Body...)
			}
		case "claim", "revoke":
			cp := *rec.Claim
			s.claims[cp.KeyDigest] = &cp
			s.indexClaim(cp.KeyDigest, cp.ArtifactDigest)
		default:
			return fmt.Errorf("artifactresolver: unknown record %q", rec.Type)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	_, err := s.file.Seek(0, 2)
	return err
}

func (s *Store) indexClaim(keyDigest, artifactDigest string) {
	for _, k := range s.byArtifact[artifactDigest] {
		if k == keyDigest {
			return
		}
	}
	s.byArtifact[artifactDigest] = append(s.byArtifact[artifactDigest], keyDigest)
}

func (s *Store) append(rec storedLine) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := s.file.Write(line); err != nil {
		return fmt.Errorf("artifactresolver: write: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("artifactresolver: sync: %w", err)
	}
	return nil
}

// Close closes the store file.
func (s *Store) Close() error { return s.file.Close() }

// RecordArtifact publishes immutable bytes under a ComputationKey.
// Idempotent for byte-identical republication; the same key binding
// different bytes fails closed (nondeterministic production is detected,
// never aliased).
func (s *Store) RecordArtifact(key ComputationKey, body []byte, receipt string, cost int64) (*ArtifactRecord, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if receipt == "" {
		return nil, fmt.Errorf("%w: receipt", ErrMissingMeta)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kd := key.Digest()
	if prev, ok := s.arts[kd]; ok {
		if prev.ArtifactDigest == DigestBytes(body) {
			return prev, nil
		}
		return nil, fmt.Errorf("%w: key %s binds two byte-distinct artifacts", ErrConflict, kd[:16])
	}
	a := ArtifactRecord{CompDigest: kd, CompCanonical: string(key.Canonical()),
		ArtifactDigest: DigestBytes(body), ReceiptID: receipt, CostUnits: cost,
		CompDeps: key.CanonicalDeps(), ProducedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.append(storedLine{Type: "artifact", Art: &a, Body: body}); err != nil {
		return nil, err
	}
	cp := a
	s.arts[kd] = &cp
	s.bodies[a.ArtifactDigest] = append([]byte(nil), body...)
	return s.arts[kd], nil
}

// RecordVerification appends a verification claim. History is preserved;
// the latest claim per key governs resolution.
func (s *Store) RecordVerification(vk VerificationKey, status ClaimStatus, evidence string, authVersion uint64) (*VerificationClaim, error) {
	if err := vk.Validate(); err != nil {
		return nil, err
	}
	if status != ClaimAccepted && status != ClaimRejected && status != ClaimUnknown {
		return nil, fmt.Errorf("artifactresolver: bad status %q", status)
	}
	if evidence == "" {
		return nil, fmt.Errorf("%w: evidence", ErrMissingMeta)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := VerificationClaim{KeyDigest: vk.Digest(), ArtifactDigest: vk.ArtifactDigest,
		Status: status, EvidenceID: evidence, VerifiedAt: time.Now().UTC().Format(time.RFC3339Nano),
		AuthVersion: authVersion}
	if err := s.append(storedLine{Type: "claim", Claim: &c}); err != nil {
		return nil, err
	}
	cp := c
	s.claims[c.KeyDigest] = &cp
	s.indexClaim(c.KeyDigest, c.ArtifactDigest)
	return s.claims[c.KeyDigest], nil
}

// RevokeVerification marks a claim revoked without touching artifact bytes
// or history. Duplicate and already-revoked deliveries are idempotent
// no-ops; unknown keys are an error (fail-closed, never silent).
func (s *Store) RevokeVerification(keyDigest, supersededBy string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.claims[keyDigest]
	if !ok {
		return false, fmt.Errorf("artifactresolver: unknown claim %.16s", keyDigest)
	}
	if c.Revoked {
		return false, nil
	}
	cp := *c
	cp.Revoked = true
	cp.SupersededBy = supersededBy
	if err := s.append(storedLine{Type: "revoke", Claim: &cp}); err != nil {
		return false, err
	}
	s.claims[keyDigest] = &cp
	return true, nil
}

// LookupArtifact returns the record for an exact ComputationKey digest.
func (s *Store) LookupArtifact(compDigest string) (*ArtifactRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.arts[compDigest]
	return a, ok
}

// LookupClaim returns the latest claim for a VerificationKey digest.
func (s *Store) LookupClaim(keyDigest string) (*VerificationClaim, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.claims[keyDigest]
	return c, ok
}

// ClaimKeysForArtifact lists claim key digests bound to artifact bytes.
// Audit aid only; never a validity input.
func (s *Store) ClaimKeysForArtifact(digest string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.byArtifact[digest]...)
}

// Body returns stored bytes by artifact digest.
func (s *Store) Body(digest string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bodies[digest]
	return b, ok
}

// CorruptBody replaces stored bytes without updating any digest. It exists
// solely to let tests exercise corruption handling (integrity failures must
// resolve RECOMPUTE or UNKNOWN, never VERIFY). Production callers must never
// mutate bytes outside RecordArtifact.
func (s *Store) CorruptBody(digest string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies[digest] = body
}

// Stats reports record counts and bytes on disk.
func (s *Store) Stats() (arts, claims int, bytesOnDisk int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	arts, claims = len(s.arts), len(s.claims)
	if fi, err := os.Stat(s.path); err == nil {
		bytesOnDisk = fi.Size()
	}
	return arts, claims, bytesOnDisk
}
