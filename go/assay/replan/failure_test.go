package replan

import (
	"errors"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
)

// failure_test.go: Phase 7 — verifier rejection, op failure, cost overrun,
// budget cuts, and mid-run revocation. Failures are first-class results:
// rescue via legal suffix, or explicit NoLegalSuffix.

// TestVerifyFailureRescue: lint's verifier starts failing after compile.
// Staged becomes illegal (lint must execute, cannot verify); R2 switches to
// direct and completes. R1-frozen fails explicitly with terminal rejected.
func TestVerifyFailureRescue(t *testing.T) {
	mkSched := func() []ScheduledTrigger {
		return []ScheduledTrigger{{AfterNode: "compile", Event: Event{
			ID: "lint-unverifiable", Kind: TrigVerifyFailed, Op: "d2.lint",
		}}}
	}
	ws := freshD2(94, 40) // staged start
	rt := testRuntime(t, ws, mkSched())
	rep, err := rt.Run("rescue")
	if err != nil {
		t.Fatalf("R2 failed: %v", err)
	}
	if !rep.TerminalOK || rep.ActivePlan != "d2-direct" {
		t.Fatalf("no rescue: %+v", rep)
	}
	if len(rep.Switches) != 1 {
		t.Fatalf("expected 1 switch: %+v", rep.Switches)
	}
	if want := oraclePlan(t, ws, "d2-direct"); rep.FinalBytes != want {
		t.Fatalf("rescued final %s != direct oracle %s", rep.FinalBytes, want)
	}
	// R1 frozen: lint executes, verification fails, downstream blocked,
	// terminal rejected — retained as explicit failure, not a hang.
	rt1 := testRuntime(t, freshD2(94, 40), mkSched())
	rt1.Frozen = true
	rep1, err := rt1.Run("frozen")
	if err != nil {
		t.Fatalf("R1 run errored (want clean explicit failure): %v", err)
	}
	if rep1.TerminalOK {
		t.Fatal("R1 should not accept with failing verifier")
	}
	t.Logf("R2 rescued spent=%d | R1 explicit failure (terminal rejected)", rep.SpentUnits)
}

// TestOpFailureRescue: lint op errors persistently after start. Same rescue
// shape via failed-op exclusion.
func TestOpFailureRescue(t *testing.T) {
	sched := []ScheduledTrigger{{AfterNode: "identity", Event: Event{
		ID: "lint-down", Kind: TrigOpFailed, Op: "d2.lint",
	}}}
	ws := freshD2(95, 40)
	rt := testRuntime(t, ws, sched)
	rep, err := rt.Run("rescue")
	if err != nil {
		t.Fatalf("R2 failed: %v", err)
	}
	if !rep.TerminalOK || rep.ActivePlan != "d2-direct" {
		t.Fatalf("no rescue: %+v", rep)
	}
}

// TestCostOverrunSwitch: staged start; aggregate cost spikes 10x mid-run.
// R2 re-quotes and switches to direct; R1 pays the spike.
func TestCostOverrunSwitch(t *testing.T) {
	sched := []ScheduledTrigger{{AfterNode: "compile", Event: Event{
		ID: "agg-spike", Kind: TrigCostChanged, Node: "aggregate", NewCost: 60,
	}}}
	ws := freshD2(96, 40)
	rt := testRuntime(t, ws, sched)
	rep, err := rt.Run("overrun")
	if err != nil {
		t.Fatalf("R2 failed: %v", err)
	}
	if !rep.TerminalOK || rep.ActivePlan != "d2-direct" {
		t.Fatalf("no overrun switch: %+v", rep)
	}
	rt1 := testRuntime(t, freshD2(96, 40), sched)
	rt1.Frozen = true
	rep1, err := rt1.Run("frozen")
	if err != nil || !rep1.TerminalOK {
		t.Fatalf("R1 failed: %v", err)
	}
	t.Logf("R2 spent=%d | R1 spent=%d (pays spike at live cost)", rep.SpentUnits, rep1.SpentUnits)
	if rep.SpentUnits >= rep1.SpentUnits {
		t.Fatalf("overrun switch saved nothing: %d vs %d", rep.SpentUnits, rep1.SpentUnits)
	}
}

// TestBudgetCut: staged start with tight budget; mid-run budget shrinks below
// staged-remaining but still fits direct → switch. Second cut below everything
// → explicit NoLegalSuffix (negative result, history preserved).
func TestBudgetCut(t *testing.T) {
	ws := freshD2(97, 40)
	// After compile (spent 12): a cost update restores direct to 19 while the
	// budget drops to 35. Staged remaining 25 (total 37 > 35, illegal);
	// direct remaining 22 (total 34 <= 35, legal) → switch.
	sched := []ScheduledTrigger{
		{AfterNode: "compile", Event: Event{ID: "reprieve", Kind: TrigCostChanged,
			Node: "direct", NewCost: 19}},
		{AfterNode: "compile", Event: Event{ID: "cut1", Kind: TrigBudgetReduced, Budget: 35}},
	}
	rt := testRuntime(t, ws, sched)
	rt.Budget = 100
	rep, err := rt.Run("budget")
	if err != nil {
		t.Fatalf("R2 failed: %v", err)
	}
	if !rep.TerminalOK || rep.ActivePlan != "d2-direct" {
		t.Fatalf("no budget switch: %+v", rep)
	}
	// Lethal cut: no suffix fits → NoLegalSuffix with spent preserved.
	rt2 := testRuntime(t, freshD2(97, 40), []ScheduledTrigger{{AfterNode: "compile",
		Event: Event{ID: "cut0", Kind: TrigBudgetReduced, Budget: 5}}})
	rt2.Budget = 100
	_, err = rt2.Run("doomed")
	var nls *NoLegalSuffix
	if !errors.As(err, &nls) {
		t.Fatalf("want NoLegalSuffix, got %v", err)
	}
	t.Logf("doomed run: %v", nls)
}

// TestRevokedReuseReplan: compile's outcome is revoked after unit-setup
// completes. Re-execution republishes (PURE); run completes; final equals
// same-plan oracle under the post-revocation world.
func TestRevokedReuseReplan(t *testing.T) {
	ws := freshD2(98, 40)
	rt := testRuntime(t, ws, nil)
	// Warm the store, find compile's job, then start the measured run on the
	// SAME store so revocation has something to revoke.
	staged, _ := ws.Plans()
	warm, err := rt.Ex.Run(staged, "P2", "warm", 0)
	if err != nil || !warm.TerminalOK {
		t.Fatalf("warmup failed: %v", err)
	}
	var compileJob string
	for j := range rt.Ex.Outcomes {
		if contains(j, "/compile/") {
			compileJob = j
		}
	}
	if compileJob == "" {
		t.Fatal("compile job not found")
	}
	rt2sched := []ScheduledTrigger{{AfterNode: "unit-setup", Event: Event{
		ID: "revoke-compile", Kind: TrigArtifactRevoked, JobID: compileJob,
	}}}
	// Fresh runtime over the SAME executor/store (revocation targets the real
	// warmed history above).
	rt3 := NewRuntime(rt.Ex, freshD2(98, 40), func(w WorldState) []plan.PhysicalPlan {
		s, d := w.Plans()
		return []plan.PhysicalPlan{s, d}
	}, "")
	rt3.Schedule = rt2sched
	rep, err := rt3.Run("revoked")
	if err != nil {
		t.Fatalf("R2 failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("terminal rejected after revocation recovery")
	}
	if want := oraclePlan(t, freshD2(98, 40), "d2-staged"); rep.FinalBytes != want {
		t.Fatalf("post-revocation final %s != oracle %s", rep.FinalBytes, want)
	}
	t.Logf("revocation recovery spent=%d switches=%d", rep.SpentUnits, len(rep.Switches))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
