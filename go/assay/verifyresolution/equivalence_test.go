package verifyresolution

import (
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// equivalence_test.go: Phase 3 — map V1 records into the split model
// (mapping lives HERE, test-only; no production migration path), detect
// collapse collisions, and reproduce V1 validity decisions.
// Hard gate: the split must never make byte-distinct computations identical.

// mapV1 converts one V1 execution+artifact into split records. The V1
// verifier contract moves OUT of computation identity into the
// VerificationKey; the V1 executor stays (treated as generating).
// Returns an error (refusal) when conversion would be lossy/ambiguous.
func mapV1(t *testing.T, s *Store, auth *outcomeTable, ek reuse.ExecutionKey,
	art reuse.Artifact) (ComputationKey, VerificationKey, error) {
	t.Helper()
	var compDeps []Dep
	for _, d := range ek.Deps {
		compDeps = append(compDeps, Dep{Name: d.Name, Digest: d.Digest})
	}
	ck := ComputationKey{Operation: ek.Operation, OpVersion: ek.OperationVersion,
		Inputs:   []Dep{{Name: "input", Digest: ek.InputDigest}},
		CompDeps: compDeps, Executor: ek.Executor, Env: ek.EnvDigest,
		Generation: ek.PolicyDigest}
	if err := ck.Validate(); err != nil {
		return ComputationKey{}, VerificationKey{}, err
	}
	vk := VerificationKey{ArtifactDigest: art.ArtifactDigest,
		Contract: ek.VerifierContract, VerifierID: "v1-outcome", VerifierVer: "1"}
	if err := vk.Validate(); err != nil {
		return ComputationKey{}, VerificationKey{}, err
	}
	// Collision gate: same ComputationKey with different bytes refuses.
	// Identical bytes merge onto one ArtifactRecord (rotation case) while
	// each distinct VerificationKey still gets its own claim.
	if prev, ok := s.LookupArtifact(ck.Digest()); ok {
		if prev.ArtifactDigest != art.ArtifactDigest {
			return ComputationKey{}, VerificationKey{},
				ErrConflict
		}
		status := claimStatusOf(art.Verification)
		ev := "v1-" + art.OutcomeJobID
		auth.accept(ev, art.OutcomeVersion)
		if _, err := s.RecordClaim(vk, status, ev, art.OutcomeVersion); err != nil {
			return ComputationKey{}, VerificationKey{}, err
		}
		return ck, vk, nil
	}
	body, ok := artBodies[art.ArtifactDigest]
	if !ok {
		return ComputationKey{}, VerificationKey{}, ErrMissingMeta
	}
	if _, err := s.PublishArtifact(ck, body, art.ReceiptID, int64(art.ActualCostUSD*1000)); err != nil {
		return ComputationKey{}, VerificationKey{}, err
	}
	status := claimStatusOf(art.Verification)
	ev := "v1-" + art.OutcomeJobID
	auth.accept(ev, art.OutcomeVersion)
	if _, err := s.RecordClaim(vk, status, ev, art.OutcomeVersion); err != nil {
		return ComputationKey{}, VerificationKey{}, err
	}
	return ck, vk, nil
}

// claimStatusOf maps V1 verification to claim status.
func claimStatusOf(v reuse.Verification) ClaimStatus {
	if v == reuse.VerificationAccepted {
		return ClaimAccepted
	} else if v == reuse.VerificationRejected {
		return ClaimRejected
	}
	return ClaimUnknown
}

// artBodies carries V1-side bytes for the mapping (test fixture table).
var artBodies = map[string][]byte{}

func v1Key(input, contract string, deps ...reuse.Dep) reuse.ExecutionKey {
	return reuse.ExecutionKey{Operation: "w1.validate", OperationVersion: "v3",
		InputDigest: input, Deps: deps, Executor: "toolchain/go1.22",
		EnvDigest: DigestString("env"), VerifierContract: contract,
		PolicyDigest: DigestString("p")}
}

func v1Publish(t *testing.T, vs *reuse.Store, bodies map[string][]byte, ek reuse.ExecutionKey,
	artifact []byte, job string, ver uint64) reuse.Artifact {
	t.Helper()
	a, err := vs.Publish(ek, reuse.PublishBody{ArtifactDigest: DigestBytes(artifact),
		Verification: reuse.VerificationAccepted, EvidenceID: "ev-" + job,
		ReceiptID: "rcpt-" + job, OutcomeJobID: job, OutcomeVersion: ver})
	if err != nil {
		t.Fatal(err)
	}
	bodies[a.ArtifactDigest] = artifact
	for k, v := range bodies {
		artBodies[k] = v
	}
	return *a
}

// TestV1Equivalence maps V1 rotation histories (same computation, verifier
// v1→v2, deterministic identical bytes) into ONE ArtifactRecord + two claims.
func TestV1Equivalence(t *testing.T) {
	vs, err := reuse.Open(filepath.Join(t.TempDir(), "v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer vs.Close()
	s, err := Open(filepath.Join(t.TempDir(), "split.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth := newOutcomeTable()
	bodies := map[string][]byte{}
	in := DigestString("tree-1")
	art1 := v1Publish(t, vs, bodies, v1Key(in, DigestString("ver/v1"),
		reuse.Dep{Name: "cfg", Digest: DigestString("c1")}), []byte("bytes-1"), "job1", 1)
	art2 := v1Publish(t, vs, bodies, v1Key(in, DigestString("ver/v2"),
		reuse.Dep{Name: "cfg", Digest: DigestString("c1")}), []byte("bytes-1"), "job2", 1)
	ck1, vk1, err := mapV1(t, s, auth, v1Key(in, DigestString("ver/v1"),
		reuse.Dep{Name: "cfg", Digest: DigestString("c1")}), art1)
	if err != nil {
		t.Fatal(err)
	}
	ck2, vk2, err := mapV1(t, s, auth, v1Key(in, DigestString("ver/v2"),
		reuse.Dep{Name: "cfg", Digest: DigestString("c1")}), art2)
	if err != nil {
		t.Fatal(err)
	}
	// Same computation collapses to ONE key; claims stay distinct per contract.
	if ck1.Digest() != ck2.Digest() {
		t.Fatal("rotation should share ComputationKey")
	}
	if vk1.Digest() == vk2.Digest() {
		t.Fatal("distinct contracts must yield distinct VerificationKeys")
	}
	a, _ := s.LookupArtifact(ck1.Digest())
	if a.ArtifactDigest != DigestBytes([]byte("bytes-1")) {
		t.Fatal("merged artifact wrong bytes")
	}
	if len(s.ClaimKeysForArtifact(a.ArtifactDigest)) != 2 {
		t.Fatal("want two claims on one artifact")
	}
}

// TestV1CollisionRefused: mapping two byte-distinct V1 records onto one
// ComputationKey refuses (hard gate).
func TestV1CollisionRefused(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "split.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth := newOutcomeTable()
	ek := v1Key(DigestString("in"), DigestString("ver/v1"))
	artBodies[DigestBytes([]byte("bytes-A"))] = []byte("bytes-A")
	artBodies[DigestBytes([]byte("bytes-B"))] = []byte("bytes-B")
	a1 := reuse.Artifact{KeyDigest: ek.KeyDigest(), ArtifactDigest: DigestBytes([]byte("bytes-A")),
		Verification: reuse.VerificationAccepted, EvidenceID: "e1",
		ReceiptID: "r1", OutcomeJobID: "j1", OutcomeVersion: 1}
	a2 := a1
	a2.ArtifactDigest = DigestBytes([]byte("bytes-B"))
	a2.EvidenceID = "e2"
	if _, _, err := mapV1(t, s, auth, ek, a1); err != nil {
		t.Fatalf("first mapping refused: %v", err)
	}
	if _, _, err := mapV1(t, s, auth, ek, a2); err == nil {
		t.Fatal("HARD GATE FAILED: byte-distinct computations collapsed")
	} else {
		t.Logf("collision refused: %v", err)
	}
}

// TestV1DecisionReproduction replays V1 validity outcomes as split resolutions.
func TestV1DecisionReproduction(t *testing.T) {
	vs, err := reuse.Open(filepath.Join(t.TempDir(), "v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer vs.Close()
	s, err := Open(filepath.Join(t.TempDir(), "split.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth := newOutcomeTable()
	bodies := map[string][]byte{}
	in := DigestString("tree-r")
	ek := v1Key(in, DigestString("ver/v1"), reuse.Dep{Name: "cfg", Digest: DigestString("c1")})
	art := v1Publish(t, vs, bodies, ek, []byte("out-r"), "jobR", 1)
	ck, vk, err := mapV1(t, s, auth, ek, art)
	if err != nil {
		t.Fatal(err)
	}
	res := (&Resolver{Store: s}).Resolve(ck, vk,
		map[string]string{"input": in, "cfg": DigestString("c1")},
		map[string]string{}, auth.lookup)
	if res.State != ResolveReuse {
		t.Fatalf("V1-VALID should reproduce as REUSE, got %s (%s)", res.State, res.Reason)
	}
	// V1-STALE by computation dep → RECOMPUTE.
	res = (&Resolver{Store: s}).Resolve(ck, vk,
		map[string]string{"input": in, "cfg": DigestString("c2")},
		map[string]string{}, auth.lookup)
	if res.State != ResolveRecompute {
		t.Fatalf("comp-dep change should be RECOMPUTE, got %s", res.State)
	}
	// V1-STALE by verifier only → VERIFY (new contract key, same bytes).
	vk2 := vk
	vk2.Contract = DigestString("ver/v2")
	res = (&Resolver{Store: s}).Resolve(ck, vk2,
		map[string]string{"input": in, "cfg": DigestString("c1")},
		map[string]string{}, auth.lookup)
	if res.State != ResolveVerify {
		t.Fatalf("verifier-only change should be VERIFY, got %s", res.State)
	}
	// V1-INVALID (revoked source) → VERIFY (bytes intact).
	auth.revoke("v1-jobR")
	res = (&Resolver{Store: s}).Resolve(ck, vk,
		map[string]string{"input": in, "cfg": DigestString("c1")},
		map[string]string{}, auth.lookup)
	if res.State != ResolveVerify {
		t.Fatalf("revoked source should be VERIFY, got %s", res.State)
	}
	// V1-UNKNOWN (evidence authority gone) → UNKNOWN.
	auth2 := newOutcomeTable()
	res = (&Resolver{Store: s}).Resolve(ck, vk,
		map[string]string{"input": in, "cfg": DigestString("c1")},
		map[string]string{}, auth2.lookup)
	if res.State != ResolveUnknown {
		t.Fatalf("unknown authority should be UNKNOWN, got %s", res.State)
	}
}
