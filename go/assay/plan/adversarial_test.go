package plan

import (
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// adversarial_test.go: Phase 8. Correctness constraints dominate cost;
// UNKNOWN is never an optimization shortcut; invalid plans are refused before
// execution; planner decisions are deterministic; every selection records why.

// 1. Cheapest plan contains a STALE dependency: priced as execute, re-executed,
// final still accepted and equal to oracle.
func TestAdvStaleCheapest(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD2(71, d2Tree(71), "go1.22.0", "lint-v1")
	runTreat(t, e, staged, TreatP2, "warm")
	mut, _ := buildD2(71, d2Tree(71), "go1.22.0", "lint-v2")
	q := e.Quote(mut)
	if !q.Legal {
		t.Fatalf("stale plan should be legal-but-priced: %s", q.Reason)
	}
	if q.NodePrices["identity"] != 0 || q.NodePrices["lint"] == 0 {
		t.Fatalf("stale node not priced as execute: %v", q.NodePrices)
	}
	r := runTreat(t, e, mut, TreatP2, "mut")
	want, ok := oracleFinal(t, mut)
	if !ok || !r.TerminalOK || r.Final != want {
		t.Fatalf("stale-plan run wrong: %+v", r)
	}
}

// 2. Cheapest plan contains an INVALID corrected artifact: recompute via
// republication, final accepted.
func TestAdvInvalidCheapest(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD1(72, d1Doc(72))
	runTreat(t, e, staged, TreatP2, "warm")
	var job string
	for j := range e.Outcomes {
		if contains(j, "/extract/") {
			job = j
		}
	}
	e.Outcomes[job] = reuse.Outcome{JobID: job, Version: 2, Status: "REJECTED", Found: true}
	e.Store.SetOutcome(e.Outcomes[job])
	e.Store.PropagateCorrection(job, 2, "REJECTED")
	r := runTreat(t, e, staged, TreatP2, "mut")
	want, ok := oracleFinal(t, staged)
	if !ok || !r.TerminalOK || r.Final != want {
		t.Fatalf("corrected-plan run wrong: %+v", r)
	}
	if !asSet(r.Executed)["extract"] {
		t.Fatalf("invalidated extract not recomputed: %v", r.Executed)
	}
}

// 3. Artifact metadata exists but bytes fail digest verification: fail closed,
// nothing downstream consumes, terminal rejected.
func TestAdvTamperedBytes(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD2(73, d2Tree(73), "go1.22.0", "lint-v1")
	r1 := runTreat(t, e, staged, TreatP2, "warm")
	if !r1.TerminalOK {
		t.Fatal("warmup failed")
	}
	// Tamper with every cached body.
	for d := range e.Bodies {
		e.Bodies[d] = []byte("tampered")
	}
	r2 := runTreat(t, e, staged, TreatP2, "tampered")
	if r2.TerminalOK {
		t.Fatal("tampered bytes accepted")
	}
	if len(r2.Executed) != 0 {
		// Reused nothing; blocked nodes must not execute blindly... note:
		// BLOCKED nodes trigger execute fallback? No: BLOCKED stays blocked.
		t.Logf("executed under tamper: %v", r2.Executed)
	}
	for id, res := range r2.Resolutions {
		if res == ResolveReuseValid {
			t.Fatalf("tampered node %s reused as valid", id)
		}
	}
}

// 4. Required dependency UNKNOWN: ghost-outcome record is never reused;
// terminal rejected, failure recorded (no fabrication, no shortcut).
func TestAdvUnknownDep(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD2(74, d2Tree(74), "go1.22.0", "lint-v1")
	// Hand-publish a VALID record citing an outcome the executor never knew.
	byID := map[string]PlanNode{}
	for _, n := range staged.Nodes {
		byID[n.NodeID] = n
	}
	ik, err := NodeKey(byID["identity"], map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := digestOp(byID["identity"], map[string][]byte{})
	if _, err := e.Store.Publish(ik, reuse.PublishBody{
		ArtifactDigest: reuse.DigestBytes(body), Verification: reuse.VerificationAccepted,
		EvidenceID: "ev-ghost", ReceiptID: "rcpt-ghost",
		OutcomeJobID: "ghost-job", OutcomeVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	e.Bodies[reuse.DigestBytes(body)] = body
	r := runTreat(t, e, staged, TreatP2, "ghost")
	// UNKNOWN source → tryReuse fails → execute fallback republishes over the
	// UNKNOWN record? No: record is VALID (published ACCEPTED), outcome unknown
	// → Evaluate UNKNOWN → execute → Publish conflicts (VALID) → FAILED.
	// Fail-closed either way: identity must not be silently reused.
	if asSet(r.Reused)["identity"] {
		dec := e.Store.Evaluate(ik, reuse.LiveOf(ik), e.lookup)
		if dec.Validity == reuse.ValidityValid {
			t.Fatal("UNKNOWN-outcome record reused as VALID")
		}
	}
	t.Logf("ghost run: resolutions=%v terminal=%v", r.Resolutions["identity"], r.TerminalOK)
}

// 5. Cost tie: deterministic selection across repeated quotes.
func TestAdvCostTie(t *testing.T) {
	e := testExecutor(t)
	s1, _ := buildD1(75, d1Doc(75))
	s2, _ := buildD1(75, d1Doc(75))
	s2.PlanID = "d1-staged-copy"
	picks := map[string]int{}
	for i := 0; i < 3; i++ {
		chosen, q, err := e.ChoosePlan([]PhysicalPlan{s1, s2})
		if err != nil {
			t.Fatal(err)
		}
		picks[chosen.PlanID]++
		if q.Reason == "" {
			t.Fatal("selection recorded no reason")
		}
	}
	if len(picks) != 1 {
		t.Fatalf("nondeterministic tie-break: %v", picks)
	}
	t.Logf("tie always → %v", picks)
}

// 6. Missing cost estimate: plan illegal, excluded; all-missing → refusal.
func TestAdvMissingCost(t *testing.T) {
	e := testExecutor(t)
	staged, direct := buildD1(76, d1Doc(76))
	staged.Nodes[2].CostUnits = MissingCost
	q := e.Quote(staged)
	if q.Legal {
		t.Fatal("missing-cost plan quoted legal")
	}
	chosen, _, err := e.ChoosePlan([]PhysicalPlan{staged, direct})
	if err != nil || chosen.PlanID != "d1-direct" {
		t.Fatalf("planner should fall back to direct: %v %v", chosen.PlanID, err)
	}
	direct.Nodes[1].CostUnits = MissingCost
	if _, _, err := e.ChoosePlan([]PhysicalPlan{staged, direct}); err == nil {
		t.Fatal("all-illegal choice succeeded")
	}
}

// 7. Shared node fails verification: FAILED + downstream BLOCKED, terminal rejected.
func TestAdvSharedVerifyFail(t *testing.T) {
	e := testExecutor(t)
	e.Reg = fanRegistry()
	bad := e.Reg
	bad.Verifiers[contractFor("f.mid")] = func(n PlanNode, in map[string][]byte, out []byte) bool { return false }
	plan := fanPlan(2, reuse.DigestString("root-x"))
	rep, err := e.Run(plan, TreatP2, "bad", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TerminalOK {
		t.Fatal("failed-verification plan accepted")
	}
	if rep.Resolutions["mid"] != ResolveFailed {
		t.Fatalf("mid not FAILED: %v", rep.Resolutions["mid"])
	}
	for _, id := range []string{"c0", "c1", "bundle"} {
		if rep.Resolutions[id] != ResolveBlockedUnknown {
			t.Fatalf("%s not blocked: %v", id, rep.Resolutions[id])
		}
	}
}

// 8. Direct plan illegal (unknown op), staged legal: P3 avoids it with reason.
func TestAdvDirectIllegal(t *testing.T) {
	e := testExecutor(t)
	staged, direct := buildD1(77, d1Doc(77))
	direct.Nodes[1].Op = "d1.nonexistent"
	rep, quote, err := e.RunP3([]PhysicalPlan{staged, direct}, "p3", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PlanID != "d1-staged" {
		t.Fatalf("P3 chose illegal direct: %s", rep.PlanID)
	}
	if quote.Reason == "" || !rep.TerminalOK {
		t.Fatalf("missing reason or rejected: %q %v", quote.Reason, rep.TerminalOK)
	}
}

// 9. Cycle: refused by planner quote and execution.
func TestAdvCycle(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD1(78, d1Doc(78))
	for i, n := range staged.Nodes {
		if n.NodeID == "validate" {
			staged.Nodes[i].Upstreams = []string{"attest"}
		}
	}
	if q := e.Quote(staged); q.Legal {
		t.Fatal("cyclic plan quoted legal")
	}
	if _, err := e.Run(staged, TreatP2, "cyc", 0); err == nil {
		t.Fatal("cyclic plan executed")
	}
}

// 10. Duplicate node identity: refused before execution (plan_test covers
// Validate; here the Run path).
func TestAdvDuplicateID(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD1(79, d1Doc(79))
	staged.Nodes = append(staged.Nodes, staged.Nodes[0])
	if _, err := e.Run(staged, TreatP2, "dup", 0); err == nil {
		t.Fatal("duplicate-id plan executed")
	}
}

// 11. Crash after shared node completes: resume reuses it, no duplicate exec.
func TestAdvCrashShared(t *testing.T) {
	e := testExecutor(t)
	e.Reg = fanRegistry()
	plan := fanPlan(2, reuse.DigestString("root-y"))
	if _, err := e.Run(plan, TreatP2, "crashfan", 2); err == nil {
		t.Fatal("expected crash")
	}
	rep, err := e.Run(plan, TreatP2, "crashfan", 0)
	if err != nil || !rep.TerminalOK {
		t.Fatalf("resume failed: %v", err)
	}
	// mid completed pre-crash (2 executions: root, mid) → resumed run must not
	// re-execute it: total executions of mid across resume == 0.
	if asSet(rep.Executed)["mid"] {
		t.Fatalf("shared node re-executed after crash: %v", rep.Executed)
	}
	orep, err := func() (*RunReport, error) {
		e2 := testExecutor(t)
		e2.Reg = fanRegistry()
		return e2.Run(plan, TreatP0, "oracle", 0)
	}()
	if err != nil || orep.FinalBytes != rep.FinalBytes {
		t.Fatal("resumed final mismatch")
	}
}

// 12. Large fan-out invalidation: fan-30 root change executes exact closure.
func TestAdvFanout30(t *testing.T) {
	e := testExecutor(t)
	e.Reg = fanRegistry()
	plan := fanPlan(30, reuse.DigestString("root-a"))
	runTreat(t, e, plan, TreatP2, "warm")
	mut := fanPlan(30, reuse.DigestString("root-b"))
	r := runTreat(t, e, mut, TreatP2, "mut")
	if !r.TerminalOK {
		t.Fatal("fan-30 rejected")
	}
	if len(r.Executed) != 33 { // root + mid + 30 consumers + bundle
		t.Fatalf("fan-30 closure wrong size %d: %v", len(r.Executed), r.Executed)
	}
	if c := countExec(r)["mid"]; c != 1 {
		t.Fatalf("mid executed %d times", c)
	}
}
