package verifyresolution

import (
	"path/filepath"
	"testing"
)

// revocation_test.go: Phase 7 — evidence revoked without pretending the
// computation never occurred. ArtifactRecord stays immutable and available;
// a new VerificationClaim is required; v2 accepts or rejects independently;
// duplicate/stale revocations are no-ops; conflicting evidence fails closed;
// history stays inspectable but cannot satisfy current requirements.

func TestRevocationKeepsArtifact(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rev.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth := newOutcomeTable()
	tr := &TRunner{Store: s, Auth: auth}
	c := Case{Workload: "W1", Seed: 61, Docs: GenSources(61, 12),
		Params: map[string]string{"extract": "v1"},
		Prior:  W1Contracts()[0], Required: W1Contracts()[5]}
	body := tr.SetupHistory(c, w1Prod, w1Ver, ClaimAccepted)
	ck := CompKey(c)
	art, ok := s.LookupArtifact(ck.Digest())
	if !ok {
		t.Fatal("artifact missing after setup")
	}
	// Revoke the v1 claim (evidence-level revocation).
	vk1 := VerifyKey(body, c.Prior, "v1", c.VerifyDeps)
	if _, err := s.RevokeClaim(vk1.Digest(), "revoked-by-authority"); err != nil {
		t.Fatal(err)
	}
	auth.revoke("ev-setup-1")
	// ArtifactRecord immutable and available.
	art2, ok := s.LookupArtifact(ck.Digest())
	if !ok || art2.ArtifactDigest != art.ArtifactDigest {
		t.Fatal("revocation mutated the artifact record")
	}
	if _, ok := s.Body(art.ArtifactDigest); !ok {
		t.Fatal("artifact bytes unavailable after revocation")
	}
	// Resolution now demands VERIFY (not REUSE, not RECOMPUTE).
	dec := (&Resolver{Store: s}).Resolve(ck, VerifyKey(body, c.Required, "v1", c.VerifyDeps),
		LiveComp(c), LiveVerify(c), auth.lookup)
	if dec.State != ResolveVerify {
		t.Fatalf("post-revocation should be VERIFY, got %s (%s)", dec.State, dec.Reason)
	}
	// v2 verifies the SAME bytes independently (accepts here).
	r1 := tr.RunR1(c, w1Prod, w1Ver)
	if r1.Failed || r1.Verdict != ClaimAccepted {
		t.Fatalf("v2 re-verification failed: %+v", r1.Decision)
	}
	if string(r1.Bytes) != string(body) {
		t.Fatal("re-verification altered bytes")
	}
	// Duplicate + stale revocation deliveries are no-ops.
	changed, err := s.RevokeClaim(vk1.Digest(), "again")
	if err != nil || changed {
		t.Fatalf("duplicate revocation not no-op: %v %v", changed, err)
	}
	// History inspectable: old claim present, revoked, unusable.
	old, ok := s.LookupClaim(vk1.Digest())
	if !ok || !old.Revoked {
		t.Fatal("history lost")
	}
	dec2 := (&Resolver{Store: s}).Resolve(ck, vk1, LiveComp(c), LiveVerify(c), auth.lookup)
	if dec2.State == ResolveReuse {
		t.Fatal("revoked claim still reusable")
	}
}

func TestRevocationRejectsIndependently(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rev2.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth := newOutcomeTable()
	tr := &TRunner{Store: s, Auth: auth}
	// W2 artifact with burn=50 (expensive); v1 accepts, tightened v6 (t1)
	// rejects burn>10 — v2's independent verdict, not inherited.
	c := Case{Workload: "W2", Seed: 62, Params: map[string]string{"transform": "t1"},
		Burn: 50, Prior: W2Contracts()[0],
		Required: ContractSpec{ID: "w2-policy", Version: "v6-tightened",
			Rules: map[string]string{"threshold": "t1", "invariant": "prefix"}}}
	body := tr.SetupHistory(c, w2Prod, w2Ver, ClaimAccepted)
	vk1 := VerifyKey(body, c.Prior, "v1", nil)
	if _, err := s.RevokeClaim(vk1.Digest(), "v1-revoked"); err != nil {
		t.Fatal(err)
	}
	auth.revoke("ev-setup-1")
	r1 := tr.RunR1(c, w2Prod, w2Ver)
	if r1.Failed {
		t.Fatalf("resolution failed: %+v", r1.Decision)
	}
	if r1.Verdict != ClaimRejected {
		t.Fatalf("v2 should independently REJECT, got %s", r1.Verdict)
	}
	if r1.Decision.State != ResolveVerify {
		t.Fatalf("want VERIFY, got %s", r1.Decision.State)
	}
	if r1.ProdExec != 0 {
		t.Fatal("rejection required production (should be verify-only)")
	}
}

func TestConflictingEvidenceFailsClosed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "conf.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth := newOutcomeTable()
	tr := &TRunner{Store: s, Auth: auth}
	c := Case{Workload: "W1", Seed: 63, Docs: GenSources(63, 8),
		Params: map[string]string{"extract": "v1"},
		Prior:  W1Contracts()[0], Required: W1Contracts()[0]}
	body := tr.SetupHistory(c, w1Prod, w1Ver, ClaimAccepted)
	// Conflicting authority: same evidence ID at a different version.
	auth.m["ev-setup-1"] = Authority{Version: 7, Status: "ACCEPTED", Found: true}
	ck := CompKey(c)
	vk := VerifyKey(body, c.Required, "v1", nil)
	dec := (&Resolver{Store: s}).Resolve(ck, vk, LiveComp(c), LiveVerify(c), auth.lookup)
	if dec.State == ResolveReuse {
		t.Fatal("conflicting evidence treated as reusable")
	}
	t.Logf("conflict → %s (%s)", dec.State, dec.Reason)
}
