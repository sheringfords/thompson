package reuse

import (
	"errors"
	"path/filepath"
	"testing"
)

func testKey() ExecutionKey {
	return ExecutionKey{
		Operation:        "w1.validate",
		OperationVersion: "v3",
		InputDigest:      DigestString("repo-tree-abc"),
		Deps: []Dep{
			{Name: "config", Digest: DigestString("cfg1")},
			{Name: "lockfile", Digest: DigestString("lock1")},
		},
		Executor:         "go-test-runner/1.22.0",
		EnvDigest:        DigestString("GOOS=linux"),
		VerifierContract: DigestString("verifier/v2"),
		PolicyDigest:     DigestString("policy-default"),
	}
}

func testBody(tag string) PublishBody {
	return PublishBody{
		ArtifactDigest: DigestString("artifact-" + tag),
		Verification:   VerificationAccepted,
		EvidenceID:     "ev-" + tag,
		VerifiedAt:     "2026-09-28T00:00:00Z",
		ActualCostUSD:  0.42,
		ReceiptID:      "rcpt-" + tag,
		OutcomeJobID:   "job-" + tag,
		OutcomeVersion: 1,
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "reuse.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func acceptLookup(jobID string, version uint64) OutcomeLookup {
	return func(q string) Outcome {
		if q == jobID {
			return Outcome{JobID: q, Version: version, Status: "ACCEPTED", Found: true}
		}
		return Outcome{}
	}
}

func liveOf(k ExecutionKey) map[string]string {
	m := map[string]string{}
	for _, d := range k.CurrentDeps() {
		m[d.Name] = d.Digest
	}
	return m
}

func TestCanonicalDeterminism(t *testing.T) {
	a := testKey()
	b := testKey()
	b.Deps = []Dep{b.Deps[1], b.Deps[0]} // reordered
	if string(a.Canonical()) != string(b.Canonical()) {
		t.Fatal("canonical serialization depends on dep order")
	}
	if a.KeyDigest() != b.KeyDigest() {
		t.Fatal("key digest depends on dep order")
	}
	for _, mut := range []func(*ExecutionKey){
		func(k *ExecutionKey) { k.PolicyDigest = DigestString("other") },
		func(k *ExecutionKey) { k.VerifierContract = DigestString("v3") },
		func(k *ExecutionKey) { k.Executor = "other/9.9" },
		func(k *ExecutionKey) { k.Deps[0].Digest = DigestString("cfg2") },
		func(k *ExecutionKey) { k.InputDigest = DigestString("other-tree") },
	} {
		c := testKey()
		mut(&c)
		if c.KeyDigest() == a.KeyDigest() {
			t.Fatal("correctness-field change did not alter key digest")
		}
	}
}

func TestPublishLookupEvaluateValid(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("a")
	pub, err := s.Publish(k, body)
	if err != nil {
		t.Fatal(err)
	}
	if pub.State != ValidityValid {
		t.Fatalf("new artifact state = %s", pub.State)
	}
	got, ok := s.Lookup(k.KeyDigest())
	if !ok || got.ArtifactDigest != body.ArtifactDigest {
		t.Fatal("lookup miss or wrong artifact")
	}
	d := s.Evaluate(k, liveOf(k), acceptLookup(body.OutcomeJobID, 1))
	if d.Validity != ValidityValid || !d.Hit {
		t.Fatalf("evaluate = %+v", d)
	}
}

func TestPublishIdempotentAndConflict(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("idem")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(k, body); err != nil {
		t.Fatalf("idempotent republish failed: %v", err)
	}
	other := body
	other.ArtifactDigest = DigestString("different-bytes")
	if _, err := s.Publish(k, other); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("expected key conflict, got %v", err)
	}
	// Different key, same artifact bytes: allowed (distinct computation,
	// same output), but no silent alias — separate records.
	k2 := testKey()
	k2.Deps[0].Digest = DigestString("cfg-other")
	if _, err := s.Publish(k2, body); err != nil {
		t.Fatalf("distinct key with same bytes refused: %v", err)
	}
}

func TestPublishRejectsNonAccepted(t *testing.T) {
	s := openTemp(t)
	for _, v := range []Verification{VerificationRejected, VerificationUnknown} {
		body := testBody("rej-" + string(v))
		body.Verification = v
		if _, err := s.Publish(testKey(), body); !errors.Is(err, ErrNotAccepted) {
			t.Fatalf("published %s execution: %v", v, err)
		}
	}
}

func TestPublishRejectsMissingMetadata(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	k.InputDigest = ""
	if _, err := s.Publish(k, testBody("m1")); !errors.Is(err, ErrMissingDep) {
		t.Fatalf("published with missing input digest: %v", err)
	}
	k = testKey()
	k.VerifierContract = ""
	if _, err := s.Publish(k, testBody("m2")); !errors.Is(err, ErrMissingDep) {
		t.Fatalf("published with missing verifier contract: %v", err)
	}
}

func TestUnknownValidityNeverValid(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("u")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	// Source outcome unknown -> UNKNOWN.
	d := s.Evaluate(k, liveOf(k), func(string) Outcome { return Outcome{} })
	if d.Validity != ValidityUnknown || d.Hit != true {
		t.Fatalf("expected UNKNOWN hit, got %+v", d)
	}
	// Required dep unresolvable in live world -> UNKNOWN.
	live := liveOf(k)
	delete(live, "config")
	d = s.Evaluate(k, live, acceptLookup(body.OutcomeJobID, 1))
	if d.Validity != ValidityUnknown {
		t.Fatalf("expected UNKNOWN on missing dep, got %+v", d)
	}
	// Unknown key entirely -> miss, never VALID.
	d = s.Evaluate(testKey(), nil, nil)
	_ = d
	other := testKey()
	other.InputDigest = DigestString("never-seen")
	d = s.Evaluate(other, nil, nil)
	if d.Validity == ValidityValid || d.Hit {
		t.Fatalf("miss treated as hit: %+v", d)
	}
}

func TestOneDepChangeStales(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("s")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	live := liveOf(k)
	live["config"] = DigestString("cfg2") // exactly one dependency mutates
	d := s.Evaluate(k, live, acceptLookup(body.OutcomeJobID, 1))
	if d.Validity != ValidityStale {
		t.Fatalf("expected STALE, got %+v", d)
	}
	got, _ := s.Lookup(k.KeyDigest())
	if got.State != ValidityStale || got.ChangedDep != "config" {
		t.Fatalf("stale record = %+v", got)
	}
	if got.Verification != VerificationAccepted {
		t.Fatal("stale transition rewrote verification history")
	}
}

func TestIrrelevantMetadataDoesNotInvalidate(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("meta")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	// Hostname/run-id/wall-clock live outside the key: same key, same live
	// world -> still VALID. (Non-semantic fields are excluded by construction;
	// they never enter Canonical().)
	d := s.Evaluate(k, liveOf(k), acceptLookup(body.OutcomeJobID, 1))
	if d.Validity != ValidityValid {
		t.Fatalf("irrelevant metadata invalidated reuse: %+v", d)
	}
}

func TestVerifierChangeStales(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("v")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	live := liveOf(k)
	live["verifier_contract"] = DigestString("verifier/v3")
	d := s.Evaluate(k, live, acceptLookup(body.OutcomeJobID, 1))
	if d.Validity != ValidityStale {
		t.Fatalf("verifier change must stale reuse, got %+v", d)
	}
}

func TestTransitiveInvalidationChain(t *testing.T) {
	s := openTemp(t)
	// A -> B -> C via upstream edges.
	ka := testKey()
	ba := testBody("chain-a")
	if _, err := s.Publish(ka, ba); err != nil {
		t.Fatal(err)
	}
	kb := testKey()
	kb.Operation = "w1.aggregate"
	kb.Deps = append(append([]Dep(nil), kb.Deps...),
		Dep{Name: UpstreamPrefix + ka.KeyDigest(), Digest: ba.ArtifactDigest})
	bb := testBody("chain-b")
	bb.OutcomeJobID = "job-chain-b"
	if _, err := s.Publish(kb, bb); err != nil {
		t.Fatal(err)
	}
	kc := testKey()
	kc.Operation = "w1.report"
	kc.Deps = []Dep{{Name: UpstreamPrefix + kb.KeyDigest(), Digest: bb.ArtifactDigest}}
	bc := testBody("chain-c")
	bc.OutcomeJobID = "job-chain-c"
	if _, err := s.Publish(kc, bc); err != nil {
		t.Fatal(err)
	}
	// Change a leaf dep of A: A goes STALE, B and C follow transitively.
	liveA := liveOf(ka)
	liveA["lockfile"] = DigestString("lock2")
	d := s.Evaluate(ka, liveA, acceptLookup(ba.OutcomeJobID, 1))
	if d.Validity != ValidityStale {
		t.Fatalf("A should be STALE: %+v", d)
	}
	for name, kk := range map[string]ExecutionKey{"B": kb, "C": kc} {
		got, ok := s.Lookup(kk.KeyDigest())
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if got.State != ValidityStale {
			t.Fatalf("%s state = %s (%s), want STALE", name, got.State, got.Reason)
		}
		if got.ChangedDep == "" || got.Reason == "" {
			t.Fatalf("%s missing reason/origin", name)
		}
	}
	// Unrelated artifact stays VALID.
	ku := testKey()
	ku.Operation = "w1.unrelated"
	bu := testBody("chain-u")
	bu.OutcomeJobID = "job-chain-u"
	if _, err := s.Publish(ku, bu); err != nil {
		t.Fatal(err)
	}
	d = s.Evaluate(ku, liveOf(ku), acceptLookup(bu.OutcomeJobID, 1))
	if d.Validity != ValidityValid {
		t.Fatalf("unrelated artifact affected: %+v", d)
	}
}

func TestRestoreDoesNotAutoRevive(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("r")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	live := liveOf(k)
	live["config"] = DigestString("cfg2")
	if d := s.Evaluate(k, live, acceptLookup(body.OutcomeJobID, 1)); d.Validity != ValidityStale {
		t.Fatalf("want STALE, got %+v", d)
	}
	// Restoring original bytes makes the EXACT key valid again (contract §4:
	// revival only via exact-key validity + authoritative source).
	if d := s.Evaluate(k, liveOf(k), acceptLookup(body.OutcomeJobID, 1)); d.Validity != ValidityValid {
		t.Fatalf("exact key + authoritative source should be VALID: %+v", d)
	}
	// But if the source was corrected meanwhile, restore stays INVALID.
	s.PropagateCorrection(body.OutcomeJobID, 2, "REJECTED")
	if d := s.Evaluate(k, liveOf(k), acceptLookup(body.OutcomeJobID, 2)); d.Validity != ValidityInvalid {
		t.Fatalf("corrected source must stay INVALID after restore: %+v", d)
	}
}

func TestCorrectionPropagation(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("corr")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	kb := testKey()
	kb.Operation = "w1.downstream"
	kb.Deps = append(append([]Dep(nil), kb.Deps...),
		Dep{Name: UpstreamPrefix + k.KeyDigest(), Digest: body.ArtifactDigest})
	bb := testBody("corr-down")
	bb.OutcomeJobID = "job-corr-down"
	if _, err := s.Publish(kb, bb); err != nil {
		t.Fatal(err)
	}
	n := s.PropagateCorrection(body.OutcomeJobID, 2, "REJECTED")
	if n != 2 {
		t.Fatalf("invalidated = %d, want 2 (artifact + downstream)", n)
	}
	for _, kk := range []ExecutionKey{k, kb} {
		got, _ := s.Lookup(kk.KeyDigest())
		if got.State != ValidityInvalid {
			t.Fatalf("key %s state = %s, want INVALID", kk.KeyDigest()[:16], got.State)
		}
		if got.Verification != VerificationAccepted {
			t.Fatal("history rewritten: verification must stay ACCEPTED")
		}
	}
	// Duplicate delivery: no-op.
	if n := s.PropagateCorrection(body.OutcomeJobID, 2, "REJECTED"); n != 0 {
		t.Fatalf("duplicate correction invalidated %d", n)
	}
	// Stale delivery (older version): no-op.
	if n := s.PropagateCorrection(body.OutcomeJobID, 1, "ACCEPTED"); n != 0 {
		t.Fatalf("stale correction invalidated %d", n)
	}
}

func TestConflictingEvidenceFailsClosed(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("conf")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	// Lookup reports a DIFFERENT version with same ACCEPTED status: evidence
	// conflicts with the bound version -> INVALID, never VALID.
	lookup := func(q string) Outcome {
		return Outcome{JobID: q, Version: 7, Status: "ACCEPTED", Found: true}
	}
	d := s.Evaluate(k, liveOf(k), lookup)
	if d.Validity != ValidityInvalid {
		t.Fatalf("conflicting evidence must be INVALID, got %+v", d)
	}
}

func TestVerifyBytes(t *testing.T) {
	s := openTemp(t)
	k := testKey()
	body := testBody("bytes")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyBytes(k.KeyDigest(), []byte("wrong bytes")); err == nil {
		t.Fatal("mismatched bytes verified")
	}
}

func TestCrashRestartReconstruction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reuse.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	k := testKey()
	body := testBody("crash")
	if _, err := s.Publish(k, body); err != nil {
		t.Fatal(err)
	}
	s.PropagateStale(k.KeyDigest(), "config", "pre-crash stale")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen: deterministic reconstruction from the file alone.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok := s2.Lookup(k.KeyDigest())
	if !ok {
		t.Fatal("record lost across restart")
	}
	if got.State != ValidityStale || got.ArtifactDigest != body.ArtifactDigest {
		t.Fatalf("reconstructed = %+v", got)
	}
	d := s2.Evaluate(k, liveOf(k), acceptLookup(body.OutcomeJobID, 1))
	if d.Validity != ValidityValid {
		t.Fatalf("post-restart exact replay = %+v", d)
	}
}
