package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// treatments_test.go: Phase 4 (P0/P1/P2/P3) + Phase 5 (minimal recomputation).
// Equivalence rule (predeclared): byte-equality against full recomputation OF
// THE SAME physical plan; terminal ACCEPT required in every treatment.
// Cross-plan byte equality is NOT expected (different computations).

type TreatResult struct {
	Treatment   string
	Executed    []string
	Reused      []string
	Skipped     []string
	Final       string
	TerminalOK  bool
	OpWorkNS    int64
	OverheadNS  int64
	Resolutions map[string]NodeResolution
}

func runTreat(t *testing.T, e *Executor, plan PhysicalPlan, treat, runID string) *TreatResult {
	t.Helper()
	rep, err := e.Run(plan, treat, runID, 0)
	if err != nil {
		t.Fatalf("%s run failed: %v", treat, err)
	}
	return &TreatResult{Treatment: treat, Executed: rep.Executed, Reused: rep.Reused,
		Skipped: rep.Skipped, Final: rep.FinalBytes, TerminalOK: rep.TerminalOK,
		OpWorkNS: rep.OpWorkNS, OverheadNS: rep.PlanOverheadNS, Resolutions: rep.Resolutions}
}

// oracleFinal runs full recomputation on an ISOLATED store: ground truth that
// never touches the assay store.
func oracleFinal(t *testing.T, plan PhysicalPlan) (final string, ok bool) {
	t.Helper()
	e := testExecutor(t)
	rep, err := e.Run(plan, TreatP0, "oracle", 0)
	if err != nil {
		t.Fatalf("oracle failed: %v", err)
	}
	return rep.FinalBytes, rep.TerminalOK
}

func asSet(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func equalSet(a []string, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for _, s := range a {
		if !b[s] {
			return false
		}
	}
	return true
}

// TestD2Treatments exercises P0-oracle/P1/P2 on D2 staged.
func TestD2Treatments(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD2(11, d2Tree(11), "go1.22.0", "lint-v1")

	// Isolated P0 ground truth.
	wantFinal, ok := oracleFinal(t, staged)
	if !ok {
		t.Fatal("oracle not accepted")
	}
	// P2 cold on the assay store: 7 executions; unit-setup reuses lint-setup's
	// identical ExecutionKey record (predeclared duplicate-key dedup case).
	cold := runTreat(t, e, staged, TreatP2, "p2-cold")
	if !cold.TerminalOK || cold.Final != wantFinal || len(cold.Executed) != 7 {
		t.Fatalf("P2 cold: %+v want final %s", cold, wantFinal)
	}
	if !equalSet(cold.Reused, map[string]bool{"unit-setup": true}) {
		t.Fatalf("P2 cold should dedup unit-setup: %+v", cold)
	}
	// P2 warm: full eligible reuse.
	p2 := runTreat(t, e, staged, TreatP2, "p2-warm")
	if !p2.TerminalOK || p2.Final != wantFinal {
		t.Fatalf("P2 warm mismatch: %+v vs %s", p2, wantFinal)
	}
	if len(p2.Executed) != 0 || len(p2.Reused) != 8 {
		t.Fatalf("P2 warm should reuse all: %+v", p2)
	}
	// P1 same expectation (independent per-node hits).
	p1 := runTreat(t, e, staged, TreatP1, "p1-warm")
	if !p1.TerminalOK || p1.Final != wantFinal || len(p1.Executed) != 0 {
		t.Fatalf("P1 warm mismatch: %+v", p1)
	}
}

// TestD2LeafMutationClosure: lintcfg change recomputes exactly {lint, aggregate, attest}.
func TestD2LeafMutationClosure(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD2(11, d2Tree(11), "go1.22.0", "lint-v1")
	runTreat(t, e, staged, TreatP2, "base")
	mut, _ := buildD2(11, d2Tree(11), "go1.22.0", "lint-v2")
	wantFinal, ok := oracleFinal(t, mut)
	if !ok {
		t.Fatal("mutated oracle not accepted")
	}
	p2 := runTreat(t, e, mut, TreatP2, "mut-p2")
	if !p2.TerminalOK || p2.Final != wantFinal {
		t.Fatalf("P2 mutated final mismatch: %+v vs %s", p2, wantFinal)
	}
	want := map[string]bool{"lint": true, "aggregate": true, "attest": true}
	if !equalSet(p2.Executed, want) {
		t.Fatalf("closure wrong: executed=%v want %v", p2.Executed, want)
	}
}

// TestD2SharedChange: toolchain change recomputes compile + downstream, reuses identity.
func TestD2SharedChange(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD2(11, d2Tree(11), "go1.22.0", "lint-v1")
	runTreat(t, e, staged, TreatP2, "base")
	mut, _ := buildD2(11, d2Tree(11), "go1.23.0", "lint-v1")
	wantFinal, ok := oracleFinal(t, mut)
	if !ok {
		t.Fatal("mutated oracle not accepted")
	}
	p2 := runTreat(t, e, mut, TreatP2, "mut-p2")
	if !p2.TerminalOK || p2.Final != wantFinal {
		t.Fatalf("P2 shared-change mismatch: %+v", p2)
	}
	want := map[string]bool{"compile": true, "lint-setup": true, "lint": true,
		"unit": true, "aggregate": true, "attest": true}
	if !equalSet(p2.Executed, want) {
		t.Fatalf("shared closure wrong: %v", p2.Executed)
	}
	// unit-setup correctly dedups onto lint-setup's identical recompiled key.
	if !asSet(p2.Reused)["unit-setup"] {
		t.Fatalf("unit-setup should dedup: reused=%v", p2.Reused)
	}
}

// TestD1ClosureSkip: P2 skips summary (outside attest closure); P1 executes it.
func TestD1ClosureSkip(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD1(11, d1Doc(11))
	p1 := runTreat(t, e, staged, TreatP1, "p1")
	if !asSet(p1.Executed)["summary"] {
		t.Fatalf("P1 should execute whole fixed DAG: %v", p1.Executed)
	}
	e2 := testExecutor(t)
	p2 := runTreat(t, e2, staged, TreatP2, "p2")
	if asSet(p2.Executed)["summary"] {
		t.Fatalf("P2 must skip outside-closure nodes: %v", p2.Executed)
	}
	if !asSet(p2.Skipped)["summary"] {
		t.Fatalf("summary not recorded skipped: %+v", p2)
	}
	if !p1.TerminalOK || !p2.TerminalOK || p1.Final != p2.Final {
		t.Fatalf("finals differ: %s vs %s", p1.Final, p2.Final)
	}
}

// TestRepeatAndIrrelevant: unchanged rerun + job-id-only change reuse everything eligible.
func TestRepeatAndIrrelevant(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD1(11, d1Doc(11))
	first := runTreat(t, e, staged, TreatP2, "r1")
	second := runTreat(t, e, staged, TreatP2, "r2")
	if len(second.Executed) != 0 || second.Final != first.Final {
		t.Fatalf("repeat must fully reuse: %+v", second)
	}
	// Irrelevant metadata: new job id, identical node keys → full reuse.
	staged.Job.JobID = "d1-11-rerun"
	third := runTreat(t, e, staged, TreatP2, "r3")
	if len(third.Executed) != 0 || third.Final != first.Final {
		t.Fatalf("job-id change must not invalidate: %+v", third)
	}
}

// TestVerifierChangeContained: summary's own contract change recomputes only
// summary. Summary sits outside the attest closure, so this runs a
// summary-terminal plan (single-output view over the same DAG).
func TestVerifierChangeContained(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD1(11, d1Doc(11))
	runTreat(t, e, staged, TreatP2, "base")
	mut, _ := buildD1(11, d1Doc(11))
	for i, n := range mut.Nodes {
		if n.NodeID == "summary" {
			mut.Nodes[i].VerifierContract = reuse.DigestString("contract:d1.summary/v2")
			e.Reg.Verifiers[mut.Nodes[i].VerifierContract] = digestVerify
		}
	}
	mut.Terminal = "summary"
	wantFinal, ok := oracleFinal(t, mut)
	if !ok {
		t.Fatal("mutated oracle not accepted")
	}
	p2 := runTreat(t, e, mut, TreatP2, "mut-p2")
	if !p2.TerminalOK || p2.Final != wantFinal {
		t.Fatalf("verifier-change final mismatch: %+v", p2)
	}
	if !equalSet(p2.Executed, map[string]bool{"summary": true}) {
		t.Fatalf("contained verifier change over-executed: %v", p2.Executed)
	}
}

// TestCorrectionInvalidatesDownstream: revoke extract outcome, rerun.
func TestCorrectionInvalidatesDownstream(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD1(11, d1Doc(11))
	runTreat(t, e, staged, TreatP2, "base")
	// Find extract's outcome job and revoke it authoritatively.
	var extractJob string
	for id, o := range e.Outcomes {
		_ = id
		_ = o
	}
	for job := range e.Outcomes {
		// jobs look like "d1-staged/extract/jN"
		if len(job) > 16 && contains(job, "/extract/") {
			extractJob = job
		}
	}
	if extractJob == "" {
		t.Fatal("extract job not found")
	}
	e.Outcomes[extractJob] = reuse.Outcome{JobID: extractJob, Version: 2, Status: "REJECTED", Found: true}
	e.Store.SetOutcome(e.Outcomes[extractJob])
	n := e.Store.PropagateCorrection(extractJob, 2, "REJECTED")
	if n < 1 {
		t.Fatal("correction invalidated nothing")
	}
	wantFinal, ok := oracleFinal(t, staged)
	if !ok {
		t.Fatal("oracle not accepted")
	}
	p2 := runTreat(t, e, staged, TreatP2, "corr-p2")
	if !p2.TerminalOK || p2.Final != wantFinal {
		t.Fatalf("post-correction final mismatch: %+v vs %s", p2, wantFinal)
	}
	ex := asSet(p2.Executed)
	for _, id := range []string{"extract", "normalize", "aggregate", "report", "attest"} {
		if !ex[id] {
			t.Fatalf("downstream closure not recomputed: %v", p2.Executed)
		}
	}
	if ex["validate"] {
		t.Fatalf("unaffected upstream recomputed: %v", p2.Executed)
	}
}

// TestCrashResume: crash mid-run, resume same runID, no duplicate executions.
func TestCrashResume(t *testing.T) {
	e := testExecutor(t)
	staged, _ := buildD2(22, d2Tree(22), "go1.22.0", "lint-v1")
	_, err := e.Run(staged, TreatP2, "crashrun", 2)
	if err == nil {
		t.Fatal("expected injected crash")
	}
	rep, err := e.Run(staged, TreatP2, "crashrun", 0)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("resumed run not accepted")
	}
	// Union of both runs' executions: 7 executed-node progress records
	// (unit-setup dedups and is never executed).
	prog := e.loadProgress("crashrun")
	if len(prog) != 7 {
		t.Fatalf("progress has %d nodes, want 7", len(prog))
	}
	wantFinal, ok := oracleFinal(t, staged)
	if !ok {
		t.Fatal("oracle not accepted")
	}
	if rep.FinalBytes != wantFinal {
		t.Fatalf("resumed final %s != oracle %s", rep.FinalBytes, wantFinal)
	}
}

// TestP3Selection: cold picks direct, warm picks staged (D1).
func TestP3Selection(t *testing.T) {
	e := testExecutor(t)
	staged, direct := buildD1(33, d1Doc(33))
	rep, quote, err := e.RunP3([]PhysicalPlan{staged, direct}, "p3cold", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PlanID != "d1-direct" {
		t.Fatalf("cold P3 should select direct, chose %s (%s)", rep.PlanID, quote.Reason)
	}
	if !rep.TerminalOK {
		t.Fatal("cold direct run not accepted")
	}
	wantD, ok := oracleFinal(t, direct)
	if !ok {
		t.Fatal("direct oracle not accepted")
	}
	if rep.FinalBytes != wantD {
		t.Fatalf("P3 direct final %s != oracle-direct %s", rep.FinalBytes, wantD)
	}
	// Warm the staged intermediates, then root-input change is NOT needed:
	// staged fully warm → P3 must pick staged at ~0.
	e2 := testExecutor(t)
	runTreat(t, e2, staged, TreatP2, "warm")
	rep2, quote2, err := e2.RunP3([]PhysicalPlan{staged, direct}, "p3warm", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.PlanID != "d1-staged" {
		t.Fatalf("warm P3 should select staged, chose %s (%s)", rep2.PlanID, quote2.Reason)
	}
	if len(rep2.Executed) != 0 {
		t.Fatalf("warm staged should execute nothing: %v", rep2.Executed)
	}
	// Root change is the cold case (all miss): staged 30u vs direct 23u.
	// Covered by the cold assertion above; partial-warmth asymmetry
	// (lintcfg change → direct at 0u) is asserted in TestP3PartialWarmth.
}

// TestHeldOutGate: zero incorrect finals + terminal acceptance on held-out seeds.
func TestHeldOutGate(t *testing.T) {
	for _, seed := range PlanHeldOutSeeds {
		e := testExecutor(t)
		staged, direct := buildD1(seed, d1Doc(seed))
		wantFinal, ok := oracleFinal(t, staged)
		if !ok {
			t.Fatalf("held-out seed %d oracle rejected", seed)
		}
		for _, tr := range []string{TreatP1, TreatP2} {
			r := runTreat(t, e, staged, tr, tr)
			if !r.TerminalOK || r.Final != wantFinal {
				t.Fatalf("held-out seed %d %s: final %s != oracle %s ok=%v",
					seed, tr, r.Final, wantFinal, r.TerminalOK)
			}
		}
		r3, _, err := e.RunP3([]PhysicalPlan{staged, direct}, "p3", 0)
		if err != nil || !r3.TerminalOK {
			t.Fatalf("held-out seed %d P3 failed: %v", seed, err)
		}
		s2, _ := buildD2(seed, d2Tree(seed), "go1.22.0", "lint-v1")
		wantB, ok := oracleFinal(t, s2)
		if !ok {
			t.Fatalf("held-out seed %d D2 oracle rejected", seed)
		}
		rb := runTreat(t, e, s2, TreatP2, "p2b")
		if !rb.TerminalOK || rb.Final != wantB {
			t.Fatalf("held-out seed %d D2: %+v vs %s", seed, rb, wantB)
		}
	}
}

// TestP3PartialWarmth: lintcfg change leaves direct fully VALID (0u) while
// staged must recompute lint+aggregate+attest (10u) → P3 picks direct with
// zero executions and a terminal-accepted final equal to same-plan P0.
func TestP3PartialWarmth(t *testing.T) {
	e := testExecutor(t)
	staged, direct := buildD2(44, d2Tree(44), "go1.22.0", "lint-v1")
	runTreat(t, e, staged, TreatP2, "warm")
	runTreat(t, e, direct, TreatP2, "warm-direct")
	muts, mutd := buildD2(44, d2Tree(44), "go1.22.0", "lint-v2")
	rep, quote, err := e.RunP3([]PhysicalPlan{muts, mutd}, "p3mut", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PlanID != "d2-direct" {
		t.Fatalf("partial-warm P3 should select direct, chose %s (%s)", rep.PlanID, quote.Reason)
	}
	if len(rep.Executed) != 0 {
		t.Fatalf("fully-valid direct should execute nothing: %v", rep.Executed)
	}
	if !rep.TerminalOK {
		t.Fatal("P3 direct final not accepted")
	}
	wantD, ok := oracleFinal(t, mutd)
	if !ok {
		t.Fatal("mutated direct oracle not accepted")
	}
	if rep.FinalBytes != wantD {
		t.Fatalf("P3 final %s != same-plan oracle %s", rep.FinalBytes, wantD)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func writePlanReport(t *testing.T, name string, v interface{}) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile(filepath.Join("testdata", name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
