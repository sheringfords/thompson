package verifyresolution

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// store.go: durable split-identity store (stdlib only). One JSONL file with
// fsync per commit; in-memory indexes rebuilt deterministically on open.
// Artifacts keyed by ComputationKey digest; claims keyed by VerificationKey
// digest with full history (revocation appends, never rewrites).

// Store is the assay split-identity store.
type Store struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	arts   map[string]*ArtifactRecord
	claims map[string]*VerificationClaim
	// byArtifact indexes claim key digests per artifact digest (rebuilt on
	// replay; audit aid, never a validity input).
	byArtifact map[string][]string
	bodies     map[string][]byte
}

// Open creates or opens the store, replaying history deterministically.
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
			return fmt.Errorf("verifyresolution: corrupt line: %w", err)
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
			found := false
			for _, k := range s.byArtifact[cp.ArtifactDigest] {
				if k == cp.KeyDigest {
					found = true
				}
			}
			if !found {
				s.byArtifact[cp.ArtifactDigest] = append(s.byArtifact[cp.ArtifactDigest], cp.KeyDigest)
			}
		default:
			return fmt.Errorf("verifyresolution: unknown record %q", rec.Type)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	_, err := s.file.Seek(0, 2)
	return err
}

func (s *Store) append(rec storedLine) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := s.file.Write(line); err != nil {
		return fmt.Errorf("verifyresolution: write: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("verifyresolution: sync: %w", err)
	}
	return nil
}

// Close closes the store file.
func (s *Store) Close() error { return s.file.Close() }

// PublishArtifact records immutable bytes under a ComputationKey. Idempotent
// for byte-identical republication; same key with DIFFERENT bytes fails
// closed (nondeterministic generation detected, never aliased).
func (s *Store) PublishArtifact(key ComputationKey, body []byte, receipt string, cost int64) (*ArtifactRecord, error) {
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
		CompDeps: key.CanonicalDeps(), ProducedAt: "2026-09-29T00:00:00Z"}
	if err := s.append(storedLine{Type: "artifact", Art: &a, Body: body}); err != nil {
		return nil, err
	}
	cp := a
	s.arts[kd] = &cp
	s.bodies[a.ArtifactDigest] = append([]byte(nil), body...)
	return s.arts[kd], nil
}

// RecordClaim appends a verification claim (history preserved; latest wins
// per key for resolution).
func (s *Store) RecordClaim(vk VerificationKey, status ClaimStatus, evidence string, authVersion uint64) (*VerificationClaim, error) {
	if err := vk.Validate(); err != nil {
		return nil, err
	}
	if status != ClaimAccepted && status != ClaimRejected && status != ClaimUnknown {
		return nil, fmt.Errorf("verifyresolution: bad status %q", status)
	}
	if evidence == "" {
		return nil, fmt.Errorf("%w: evidence", ErrMissingMeta)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := VerificationClaim{KeyDigest: vk.Digest(), ArtifactDigest: vk.ArtifactDigest,
		Status: status, EvidenceID: evidence, VerifiedAt: "2026-09-29T00:00:00Z",
		AuthVersion: authVersion}
	if err := s.append(storedLine{Type: "claim", Claim: &c}); err != nil {
		return nil, err
	}
	cp := c
	s.claims[c.KeyDigest] = &cp
	found := false
	for _, k := range s.byArtifact[c.ArtifactDigest] {
		if k == c.KeyDigest {
			found = true
		}
	}
	if !found {
		s.byArtifact[c.ArtifactDigest] = append(s.byArtifact[c.ArtifactDigest], c.KeyDigest)
	}
	return s.claims[c.KeyDigest], nil
}

// RevokeClaim marks a claim revoked (bytes untouched; history intact).
// Duplicate and stale (already-revoked) deliveries are no-ops.
func (s *Store) RevokeClaim(keyDigest, supersededBy string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.claims[keyDigest]
	if !ok {
		return false, fmt.Errorf("verifyresolution: unknown claim %s", keyDigest[:16])
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

// CorruptBody tampers with stored bytes (adversarial fixture only).
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
