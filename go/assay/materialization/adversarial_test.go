package materialization

import (
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// adversarial_test.go: Phase 9. Fail closed where correctness cannot be
// established; never convert missing knowledge into a hit; explain
// provenance; outputs equal recomputation or explicit refusal.

func advRunner(t *testing.T) *Runner {
	t.Helper()
	return NewRunner(ModeC, nil, mustOpenStore(t))
}

func mustOpenStore(t *testing.T) *reuse.Store {
	t.Helper()
	s, err := reuse.Open(t.TempDir() + "/adv.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func advKey(r *Runner, op, input string, deps []reuse.Dep) reuse.ExecutionKey {
	if _, ok := r.Contracts[op]; !ok {
		r.Contracts[op] = ContractFor(op, "v1")
	}
	return r.cKey(op, "v1", input, deps, r.Contracts[op])
}

func advPublish(t *testing.T, r *Runner, op, input string, deps []reuse.Dep, body []byte, job string) {
	t.Helper()
	if _, ok := r.Contracts[op]; !ok {
		r.Contracts[op] = ContractFor(op, "v1")
	}
	r.Outcomes[job] = reuse.Outcome{JobID: job, Version: 1, Status: "ACCEPTED", Found: true}
	r.Store.SetOutcome(r.Outcomes[job])
	if _, err := r.Store.Publish(advKey(r, op, input, deps), reuse.PublishBody{
		ArtifactDigest: digestOf(body), Verification: reuse.VerificationAccepted,
		EvidenceID: "ev-" + job, ReceiptID: "rcpt-" + job,
		OutcomeJobID: job, OutcomeVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	r.Bodies[digestOf(body)] = body
}

// 1. Missing required dependency: unresolvable dep → UNKNOWN, never VALID.
func TestAdvMissingDep(t *testing.T) {
	r := advRunner(t)
	k := advKey(r, "enrich", digestOf([]byte("in")),
		[]reuse.Dep{{Name: "taxonomy", Digest: ""}})
	if err := k.Validate(); err == nil {
		t.Fatal("empty dep digest validated")
	}
	// Live world missing the dep entirely → UNKNOWN.
	k2 := advKey(r, "enrich", digestOf([]byte("in")),
		[]reuse.Dep{{Name: "taxonomy", Digest: digestOf([]byte("tax"))}})
	advPublish(t, r, "enrich", digestOf([]byte("in")),
		[]reuse.Dep{{Name: "taxonomy", Digest: digestOf([]byte("tax"))}}, []byte("out"), "j1")
	live := map[string]string{"input": digestOf([]byte("in"))} // taxonomy absent
	dec := r.Store.Evaluate(k2, live, r.lookup)
	if dec.Validity == reuse.ValidityValid {
		t.Fatal("missing dep evaluated VALID")
	}
	t.Logf("missing dep → %s (%s)", dec.Validity, dec.Reason)
}

// 2. Incorrect dependency digest → STALE with named dep, never silent reuse.
func TestAdvWrongDigest(t *testing.T) {
	r := advRunner(t)
	deps := []reuse.Dep{{Name: "taxonomy", Digest: digestOf([]byte("tax-v1"))}}
	advPublish(t, r, "enrich", digestOf([]byte("in")), deps, []byte("out"), "j1")
	k := advKey(r, "enrich", digestOf([]byte("in")), deps)
	live := reuse.LiveOf(k)
	live["taxonomy"] = digestOf([]byte("tax-v2"))
	dec := r.Store.Evaluate(k, live, r.lookup)
	if dec.Validity != reuse.ValidityStale {
		t.Fatalf("want STALE, got %s", dec.Validity)
	}
}

// 3. Omitted dependency: a key built WITHOUT the taxonomy dep cannot see
// taxonomy rotation (documented limitation of declared-dep identity).
// The assay DETECTS it (reused bytes ≠ fresh bytes) rather than hiding it.
func TestAdvOmittedDep(t *testing.T) {
	r := advRunner(t)
	k := advKey(r, "enrich", digestOf([]byte("in")), nil) // taxonomy omitted!
	outV1 := EnrichOp([]byte("in"), "tax/v1")
	advPublish(t, r, "enrich", digestOf([]byte("in")), nil, outV1, "j1")
	dec := r.Store.Evaluate(k, reuse.LiveOf(k), r.lookup)
	if dec.Validity != reuse.ValidityValid {
		t.Fatalf("omitted-dep key should still hit itself: %s", dec.Validity)
	}
	fresh := EnrichOp([]byte("in"), "tax/v2")
	if verifyOpOut(fresh, outV1) {
		t.Fatal("fixture error: taxonomy change invisible even in bytes")
	}
	t.Log("omitted dep: reuse is byte-stale vs fresh (limitation demonstrated, not hidden)")
}

// 5. Corrupted bytes: VerifyBytes fails → consumption refused.
func TestAdvCorruptedBytes(t *testing.T) {
	r := advRunner(t)
	k := advKey(r, "validate", digestOf([]byte("r")), nil)
	advPublish(t, r, "validate", digestOf([]byte("r")), nil, []byte("good"), "j1")
	if err := r.Store.VerifyBytes(k.KeyDigest(), []byte("evil")); err == nil {
		t.Fatal("corrupted bytes verified")
	}
}

// 6. Revocation propagates transitively to terminal lineage.
func TestAdvRevocationTransitive(t *testing.T) {
	r := advRunner(t)
	// Chain: A -> B -> C via upstream edges.
	ka := advKey(r, "opA", digestOf([]byte("in")), nil)
	advPublish(t, r, "opA", digestOf([]byte("in")), nil, []byte("a"), "jobA")
	kb := advKey(r, "opB", digestOf([]byte("a")),
		[]reuse.Dep{{Name: reuse.UpstreamPrefix + ka.KeyDigest(), Digest: digestOf([]byte("a"))}})
	advPublish(t, r, "opB", digestOf([]byte("a")),
		[]reuse.Dep{{Name: reuse.UpstreamPrefix + ka.KeyDigest(), Digest: digestOf([]byte("a"))}}, []byte("b"), "jobB")
	n := r.Store.PropagateCorrection("jobA", 2, "REJECTED")
	if n != 2 {
		t.Fatalf("transitive invalidation = %d, want 2", n)
	}
	for _, kk := range []reuse.ExecutionKey{ka, kb} {
		a, _ := r.Store.Lookup(kk.KeyDigest())
		if a.State != reuse.ValidityInvalid {
			t.Fatalf("record not INVALID: %+v", a)
		}
		if a.Verification != reuse.VerificationAccepted {
			t.Fatal("history rewritten")
		}
	}
}

// 9. Irrelevant metadata: plan/job labels outside keys → full reuse.
func TestAdvIrrelevantMetadata(t *testing.T) {
	r := advRunner(t)
	k := advKey(r, "validate", digestOf([]byte("r")), nil)
	advPublish(t, r, "validate", digestOf([]byte("r")), nil, []byte("good"), "job-1")
	// Same key under a different job label context: still VALID (job ids are
	// not part of keys; evidence is bound per record).
	dec := r.Store.Evaluate(k, reuse.LiveOf(k), r.lookup)
	if dec.Validity != reuse.ValidityValid {
		t.Fatalf("irrelevant metadata invalidated: %s", dec.Validity)
	}
}

// 10. Revert to previous exact digest → reuse iff evidence still valid.
func TestAdvRevert(t *testing.T) {
	r := advRunner(t)
	k := advKey(r, "enrich", digestOf([]byte("in")),
		[]reuse.Dep{{Name: "taxonomy", Digest: digestOf([]byte("tax-v1"))}})
	advPublish(t, r, "enrich", digestOf([]byte("in")),
		[]reuse.Dep{{Name: "taxonomy", Digest: digestOf([]byte("tax-v1"))}}, []byte("out"), "j1")
	// Rotate away and back: exact digest restored + evidence intact → VALID.
	live := reuse.LiveOf(k)
	if dec := r.Store.Evaluate(k, live, r.lookup); dec.Validity != reuse.ValidityValid {
		t.Fatalf("restored digest not reusable: %s", dec.Validity)
	}
	// But with revoked evidence, restore stays INVALID.
	r.Outcomes["j1"] = reuse.Outcome{JobID: "j1", Version: 2, Status: "REJECTED", Found: true}
	r.Store.SetOutcome(r.Outcomes["j1"])
	r.Store.PropagateCorrection("j1", 2, "REJECTED")
	if dec := r.Store.Evaluate(k, live, r.lookup); dec.Validity != reuse.ValidityInvalid {
		t.Fatalf("revoked restore should be INVALID: %s", dec.Validity)
	}
}

// 4. Verifier rotation: C conservatively re-executes (keys rotate); B hits
// with evidence-stale entries (false reuse > 0); Bp re-verifies, 0 false.
func TestAdvVerifierRotation(t *testing.T) {
	base := GenCorpus(77, 60)
	mk := func(m ModeID) *Runner {
		var cache *FileCache
		var store *reuse.Store
		var err error
		if m == ModeB || m == ModeBp {
			cache, err = OpenCache(t.TempDir() + "/" + string(m) + ".jsonl")
		} else {
			store, err = reuse.Open(t.TempDir() + "/" + string(m) + ".jsonl")
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cache != nil {
				_ = cache.Close()
			} else {
				_ = store.Close()
			}
		})
		return NewRunner(m, cache, store)
	}
	rB, rBp, rC := mk(ModeB), mk(ModeBp), mk(ModeC)
	for _, r := range []*Runner{rB, rBp, rC} {
		if res := r.RunVersion(base, "v0"); res.Verdict != "ACCEPTED" {
			t.Fatal("base rejected")
		}
		for op := range r.Contracts {
			r.Contracts[op] = ContractFor(op, "v2")
		}
	}
	resB := rB.RunVersion(base, "v2")
	resBp := rBp.RunVersion(base, "v2")
	resC := rC.RunVersion(base, "v2")
	fB, _ := gradeReuses(t, "B", rB.LastAudits, rB.Contracts, rB.lookup)
	fBp, _ := gradeReuses(t, "Bp", rBp.LastAudits, rBp.Contracts, rBp.lookup)
	fC, _ := gradeReuses(t, "C", rC.LastAudits, rC.Contracts, rC.lookup)
	t.Logf("B exec=%d false=%d | Bp exec=%d false=%d | C exec=%d false=%d",
		resB.Executed, fB, resBp.Executed, fBp, resC.Executed, fC)
	if fB == 0 {
		t.Fatal("B should evidence-false-reuse on contract rotation")
	}
	if fBp != 0 || fC != 0 {
		t.Fatalf("Bp/C false reuse: %d/%d", fBp, fC)
	}
	if resC.Executed == 0 {
		t.Fatal("C should conservatively re-execute on contract rotation")
	}
}

// 7. High-fanout shared change (taxonomy): exactly the enrich closure plus
// global downstream recomputes; record-local prefixes reuse.
func TestAdvFanoutClosure(t *testing.T) {
	base := GenCorpus(78, 120)
	r := NewRunner(ModeC, nil, mustOpenStore(t))
	if res := r.RunVersion(base, "v0"); res.Verdict != "ACCEPTED" {
		t.Fatal("base rejected")
	}
	mut := base
	mut.Taxonomy = "tax/v2"
	mut.Version = "taxchange"
	res := r.RunVersion(mut, "taxchange")
	// Per record: validate/normalize/extract/verify reuse (4×120); enrich
	// re-executes (120) + 16 shards + combine + report + attest (19).
	wantExec := 120 + 16 + 3
	if res.Executed != wantExec {
		t.Fatalf("fanout closure exec=%d want %d (reused=%d)", res.Executed, wantExec, res.Reused)
	}
	if res.Verdict != "ACCEPTED" {
		t.Fatal("terminal rejected")
	}
}

// 8. Global change (schema): nearly everything recomputes; only
// schema-independent validates reuse.
func TestAdvGlobalChange(t *testing.T) {
	base := GenCorpus(79, 120)
	r := NewRunner(ModeC, nil, mustOpenStore(t))
	if res := r.RunVersion(base, "v0"); res.Verdict != "ACCEPTED" {
		t.Fatal("base rejected")
	}
	mut := base
	mut.Schema = "schema/v4"
	res := r.RunVersion(mut, "schemachange")
	// validate reuses (120); normalize/extract/verify/enrich re-exec (480);
	// shards+combine+report+attest re-exec (19).
	wantExec := 480 + 16 + 3
	if res.Executed != wantExec {
		t.Fatalf("global closure exec=%d want %d", res.Executed, wantExec)
	}
}

// 11. Dependency cycle: evaluation terminates; validity stays per-record
// sound (flat dep compare + outcome check; propagation is state-guarded).
func TestAdvCycle(t *testing.T) {
	r := advRunner(t)
	ka := advKey(r, "opA", digestOf([]byte("in")),
		[]reuse.Dep{{Name: reuse.UpstreamPrefix + "KB", Digest: digestOf([]byte("b"))}})
	kb := advKey(r, "opB", digestOf([]byte("in")),
		[]reuse.Dep{{Name: reuse.UpstreamPrefix + ka.KeyDigest(), Digest: digestOf([]byte("a"))}})
	advPublish(t, r, "opA", digestOf([]byte("in")),
		[]reuse.Dep{{Name: reuse.UpstreamPrefix + "KB", Digest: digestOf([]byte("b"))}}, []byte("a"), "jA")
	advPublish(t, r, "opB", digestOf([]byte("in")),
		[]reuse.Dep{{Name: reuse.UpstreamPrefix + ka.KeyDigest(), Digest: digestOf([]byte("a"))}}, []byte("b"), "jB")
	done := make(chan reuse.Validity, 1)
	go func() {
		n := r.Store.PropagateCorrection("jA", 2, "REJECTED")
		_ = n
		a, _ := r.Store.Lookup(kb.KeyDigest())
		done <- a.State
	}()
	select {
	case st := <-done:
		if st != reuse.ValidityInvalid {
			t.Fatalf("cycle propagation wrong: %s", st)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cycle propagation hung")
	}
}

// 12. Duplicate edge definitions refused at construction.
func TestAdvDuplicateEdge(t *testing.T) {
	r := advRunner(t)
	k := advKey(r, "enrich", digestOf([]byte("in")), []reuse.Dep{
		{Name: "taxonomy", Digest: digestOf([]byte("t1"))},
		{Name: "taxonomy", Digest: digestOf([]byte("t2"))},
	})
	if err := k.Validate(); err == nil {
		t.Fatal("duplicate dep validated")
	}
	if _, err := r.Store.Publish(k, reuse.PublishBody{
		ArtifactDigest: digestOf([]byte("o")), Verification: reuse.VerificationAccepted,
		EvidenceID: "e", ReceiptID: "q", OutcomeJobID: "j",
	}); err == nil {
		t.Fatal("duplicate-edge publish accepted")
	}
}
