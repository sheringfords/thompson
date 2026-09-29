package artifactresolver

import (
	"path/filepath"
	"testing"
)

// resolver_test.go: ported correctness corpus (Phase 4). Adapted to the
// four-function API; semantics identical to the proven assay scenarios.
// Hard gates: zero false ACCEPTED; verification-only rotation performs zero
// artifact production; computation changes resolve RECOMPUTE; revocation
// never mutates or deletes artifact bytes.

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ar.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func compKey(op string, inputs ...Dep) ComputationKey {
	return ComputationKey{Operation: op, OpVersion: "v1", Inputs: inputs,
		Executor: "test/1.0", Env: DigestString(""), Generation: DigestString("g")}
}

func vkey(artifact []byte, contract string) VerificationKey {
	return VerificationKey{ArtifactDigest: DigestBytes(artifact),
		Contract: DigestString(contract), VerifierID: "test-verifier", VerifierVer: "v1"}
}

func acceptAuth(evidence string, v uint64) AuthorityLookup {
	return func(id string) Authority {
		if id == evidence {
			return Authority{Version: v, Status: "ACCEPTED", Found: true}
		}
		return Authority{}
	}
}

func liveOf(ck ComputationKey) map[string]string {
	m := map[string]string{}
	for _, d := range ck.CanonicalDeps() {
		m[d.Name] = d.Digest
	}
	return m
}

func publish(t *testing.T, s *Store, ck ComputationKey, body []byte, job string, auth map[string]Authority) {
	t.Helper()
	if _, err := s.RecordArtifact(ck, body, "rcpt-"+job, 0); err != nil {
		t.Fatal(err)
	}
	auth[job] = Authority{Version: 1, Status: "ACCEPTED", Found: true}
}

func claim(t *testing.T, s *Store, vk VerificationKey, st ClaimStatus, job string, v uint64) {
	t.Helper()
	if _, err := s.RecordVerification(vk, st, job, v); err != nil {
		t.Fatal(err)
	}
}

func lookup(auth map[string]Authority) AuthorityLookup {
	return func(id string) Authority {
		if a, ok := auth[id]; ok {
			return a
		}
		return Authority{}
	}
}

// TestResolutionStates covers REUSE/VERIFY/RECOMPUTE/UNKNOWN end to end.
func TestResolutionStates(t *testing.T) {
	s := testStore(t)
	auth := map[string]Authority{}
	ck := compKey("produce", Dep{Name: "in", Digest: DigestString("input-1")})
	body := []byte("artifact-bytes-1")
	publish(t, s, ck, body, "job1", auth)
	vk := vkey(body, "contract-v1")
	claim(t, s, vk, ClaimAccepted, "job1", 1)
	r := &Resolver{Store: s}

	dec := r.Resolve(ck, vk, liveOf(ck), map[string]string{}, lookup(auth))
	if dec.State != ResolveReuse {
		t.Fatalf("want REUSE, got %s (%s)", dec.State, dec.Reason)
	}
	// Verification-only rotation: new contract, same bytes → VERIFY.
	vk2 := vkey(body, "contract-v2")
	dec = r.Resolve(ck, vk2, liveOf(ck), map[string]string{}, lookup(auth))
	if dec.State != ResolveVerify {
		t.Fatalf("want VERIFY, got %s (%s)", dec.State, dec.Reason)
	}
	// Computation change → RECOMPUTE.
	live := liveOf(ck)
	live["in"] = DigestString("input-2")
	dec = r.Resolve(ck, vk, live, map[string]string{}, lookup(auth))
	if dec.State != ResolveRecompute {
		t.Fatalf("want RECOMPUTE, got %s (%s)", dec.State, dec.Reason)
	}
	// Missing artifact → RECOMPUTE.
	ck2 := compKey("produce", Dep{Name: "in", Digest: DigestString("never-seen")})
	dec = r.Resolve(ck2, vk, liveOf(ck2), map[string]string{}, lookup(auth))
	if dec.State != ResolveRecompute {
		t.Fatalf("want RECOMPUTE on miss, got %s", dec.State)
	}
	// Unknown authority → UNKNOWN, never reusable.
	dec = r.Resolve(ck, vk, liveOf(ck), map[string]string{}, lookup(map[string]Authority{}))
	if dec.State != ResolveUnknown {
		t.Fatalf("want UNKNOWN, got %s", dec.State)
	}
	// Missing dep metadata → UNKNOWN.
	bad := liveOf(ck)
	delete(bad, "in")
	dec = r.Resolve(ck, vk, bad, map[string]string{}, lookup(auth))
	if dec.State != ResolveUnknown {
		t.Fatalf("want UNKNOWN on missing dep, got %s", dec.State)
	}
}

// TestCollisionRefusal: same key, distinct bytes refuses.
func TestCollisionRefusal(t *testing.T) {
	s := testStore(t)
	ck := compKey("produce", Dep{Name: "in", Digest: DigestString("x")})
	if _, err := s.RecordArtifact(ck, []byte("bytes-a"), "r1", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordArtifact(ck, []byte("bytes-b"), "r2", 0); err == nil {
		t.Fatal("byte-distinct aliasing accepted")
	}
	// Byte-identical republication is idempotent.
	if _, err := s.RecordArtifact(ck, []byte("bytes-a"), "r3", 0); err != nil {
		t.Fatalf("idempotent republish refused: %v", err)
	}
}

// TestCorruption: tampered bytes resolve RECOMPUTE, never VERIFY.
func TestCorruption(t *testing.T) {
	s := testStore(t)
	auth := map[string]Authority{}
	ck := compKey("produce", Dep{Name: "in", Digest: DigestString("x")})
	body := []byte("good-bytes")
	publish(t, s, ck, body, "job1", auth)
	vk := vkey(body, "contract-v1")
	claim(t, s, vk, ClaimAccepted, "job1", 1)
	s.CorruptBody(DigestBytes(body), []byte("evil-bytes"))
	r := &Resolver{Store: s}
	dec := r.Resolve(ck, vk, liveOf(ck), map[string]string{}, lookup(auth))
	if dec.State == ResolveVerify || dec.State == ResolveReuse {
		t.Fatalf("corruption resolved %s (must be RECOMPUTE/UNKNOWN)", dec.State)
	}
	t.Logf("corruption → %s (%s)", dec.State, dec.Reason)
}

// TestRotationNoProduction: verification-only rotation performs zero
// artifact production in the parity harness (asserted via store stats:
// artifact count unchanged across the rotation).
func TestRotationNoProduction(t *testing.T) {
	s := testStore(t)
	auth := map[string]Authority{}
	ck := compKey("produce", Dep{Name: "in", Digest: DigestString("x")})
	body := []byte("stable-bytes")
	publish(t, s, ck, body, "job1", auth)
	claim(t, s, vkey(body, "contract-v1"), ClaimAccepted, "job1", 1)
	r := &Resolver{Store: s}
	before, _, _ := s.Stats()
	dec := r.Resolve(ck, vkey(body, "contract-v2"), liveOf(ck), map[string]string{}, lookup(auth))
	if dec.State != ResolveVerify {
		t.Fatalf("want VERIFY, got %s", dec.State)
	}
	// Simulate the VERIFY act: integrity + fresh claim, no production.
	b, ok := s.Body(dec.Artifact)
	if !ok || DigestBytes(b) != dec.Artifact {
		t.Fatal("integrity check failed")
	}
	claim(t, s, vkey(b, "contract-v2"), ClaimAccepted, "job2", 1)
	auth["job2"] = Authority{Version: 1, Status: "ACCEPTED", Found: true}
	after, _, _ := s.Stats()
	if after != before {
		t.Fatalf("rotation produced artifacts: %d -> %d", before, after)
	}
	dec2 := r.Resolve(ck, vkey(b, "contract-v2"), liveOf(ck), map[string]string{}, lookup(auth))
	if dec2.State != ResolveReuse {
		t.Fatalf("want REUSE after re-verify, got %s", dec2.State)
	}
}

// TestRevocation: claim revoked, bytes intact, re-verify required.
func TestRevocation(t *testing.T) {
	s := testStore(t)
	auth := map[string]Authority{}
	ck := compKey("produce", Dep{Name: "in", Digest: DigestString("x")})
	body := []byte("rev-bytes")
	publish(t, s, ck, body, "job1", auth)
	vk := vkey(body, "contract-v1")
	claim(t, s, vk, ClaimAccepted, "job1", 1)
	r := &Resolver{Store: s}
	changed, err := s.RevokeVerification(vk.Digest(), "authority-revoked")
	if err != nil || !changed {
		t.Fatalf("revoke failed: %v %v", changed, err)
	}
	auth["job1"] = Authority{Version: 2, Status: "REJECTED", Found: true}
	// Bytes untouched.
	if b, ok := s.Body(DigestBytes(body)); !ok || string(b) != string(body) {
		t.Fatal("revocation mutated bytes")
	}
	dec := r.Resolve(ck, vk, liveOf(ck), map[string]string{}, lookup(auth))
	if dec.State != ResolveVerify {
		t.Fatalf("want VERIFY post-revocation, got %s", dec.State)
	}
	// Duplicate + stale revocations are idempotent no-ops.
	changed, err = s.RevokeVerification(vk.Digest(), "again")
	if err != nil || changed {
		t.Fatalf("dup revoke not no-op: %v %v", changed, err)
	}
	// Unknown claim revocation errors (fail-closed).
	if _, err := s.RevokeVerification(DigestString("nope"), "x"); err == nil {
		t.Fatal("unknown revocation accepted")
	}
	// Revoked claim never reusable even if authority flips back textually.
	dec = r.Resolve(ck, vk, liveOf(ck), map[string]string{}, acceptAuth("job1", 1))
	if dec.State == ResolveReuse {
		t.Fatal("revoked claim reusable")
	}
}

// TestConflictingEvidence: same evidence ID at an unexpected version refuses REUSE.
func TestConflictingEvidence(t *testing.T) {
	s := testStore(t)
	auth := map[string]Authority{}
	ck := compKey("produce", Dep{Name: "in", Digest: DigestString("x")})
	body := []byte("conf-bytes")
	publish(t, s, ck, body, "job1", auth)
	vk := vkey(body, "contract-v1")
	claim(t, s, vk, ClaimAccepted, "job1", 1)
	r := &Resolver{Store: s}
	auth["job1"] = Authority{Version: 9, Status: "ACCEPTED", Found: true}
	dec := r.Resolve(ck, vk, liveOf(ck), map[string]string{}, lookup(auth))
	if dec.State == ResolveReuse {
		t.Fatal("conflicting evidence reusable")
	}
	t.Logf("conflict → %s (%s)", dec.State, dec.Reason)
}

// TestOldClaimNewContract: an old ACCEPTED claim never satisfies a rotated key.
func TestOldClaimNewContract(t *testing.T) {
	s := testStore(t)
	auth := map[string]Authority{}
	ck := compKey("produce", Dep{Name: "in", Digest: DigestString("x")})
	body := []byte("rot-bytes")
	publish(t, s, ck, body, "job1", auth)
	claim(t, s, vkey(body, "contract-v1"), ClaimAccepted, "job1", 1)
	r := &Resolver{Store: s}
	dec := r.Resolve(ck, vkey(body, "contract-v2"), liveOf(ck), map[string]string{}, lookup(auth))
	if dec.State == ResolveReuse {
		t.Fatal("old claim satisfied new contract")
	}
	if dec.State != ResolveVerify {
		t.Fatalf("want VERIFY, got %s", dec.State)
	}
}

// TestRestartReconstruction: close + reopen rebuilds identical state.
func TestRestartReconstruction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ar.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	auth := map[string]Authority{}
	ck := compKey("produce", Dep{Name: "in", Digest: DigestString("x")})
	body := []byte("persist-bytes")
	publish(t, s, ck, body, "job1", auth)
	vk := vkey(body, "contract-v1")
	claim(t, s, vk, ClaimAccepted, "job1", 1)
	if _, err := s.RevokeVerification(vk.Digest(), "r"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	r := &Resolver{Store: s2}
	// Reconstruct an equivalent authority view (caller-owned, like replay).
	auth2 := map[string]Authority{"job1": {Version: 2, Status: "REJECTED", Found: true}}
	dec := r.Resolve(ck, vk, liveOf(ck), map[string]string{}, lookup(auth2))
	if dec.State != ResolveVerify {
		t.Fatalf("post-restart want VERIFY, got %s (%s)", dec.State, dec.Reason)
	}
}
