package replan

import (
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
)

// midrun_test.go: Phase 5 primary experiment — D2 lint/config mid-run
// invalidation. Invalidation works by key rotation (new dependency digest ->
// new ExecutionKey): old-closure artifacts become unreachable, never consumed.

// oraclePlan runs same-plan full recomputation (isolated store).
func oraclePlan(t *testing.T, ws WorldState, planID string) string {
	t.Helper()
	rt := testRuntime(t, ws, nil)
	staged, direct := ws.Plans()
	var p plan.PhysicalPlan
	if planID == staged.PlanID {
		p = staged
	} else {
		p = direct
	}
	rep, err := rt.Ex.Run(p, plan.TreatP0, "oracle", 0)
	if err != nil || !rep.TerminalOK {
		t.Fatalf("oracle %s failed: %v", planID, err)
	}
	return rep.FinalBytes
}

func freshD2(seed uint64, directCost int) WorldState {
	ws := BaseWorldState("D2", seed)
	if directCost >= 0 {
		ws.CostOverride["direct"] = directCost
	}
	return ws
}

func lintTrigger(afterNode string) ScheduledTrigger {
	return ScheduledTrigger{AfterNode: afterNode, Event: Event{
		ID: "lint-v2", Kind: TrigDepInvalidated,
		Node: "lint", InputName: "lintcfg", NewDigest: "lint-v2",
	}}
}

// TestMidRunLintInvalidation: staged-only runtime, lintcfg changes after
// compile completes. Hard gate: fresh lint executes with new bytes; the old
// v1 artifact never satisfies any new-state node; final == staged oracle.
func TestMidRunLintInvalidation(t *testing.T) {
	ws := BaseWorldState("D2", 91)
	// Force staged start: direct priced out initially.
	ws.CostOverride["direct"] = 40
	rt := testRuntime(t, ws, []ScheduledTrigger{lintTrigger("compile")})
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		staged, _ := w.Plans()
		return []plan.PhysicalPlan{staged} // single alternative: must continue
	}
	rep, err := rt.Run("midrun")
	if err != nil {
		t.Fatalf("R2 failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("terminal rejected")
	}
	if len(rep.Switches) != 0 {
		t.Fatalf("single-alt run switched: %+v", rep.Switches)
	}
	// Final world oracle (same staged plan, lint-v2).
	wsFinal := freshD2(91, 40)
	wsFinal.LintCfg = "lint-v2"
	if want := oraclePlan(t, wsFinal, "d2-staged"); rep.FinalBytes != want {
		t.Fatalf("final %s != oracle %s", rep.FinalBytes, want)
	}
	// Hard gate: capture old lint bytes from a pre-trigger-only run and
	// require the new lint output to differ (new key, new bytes).
	rtPre := testRuntime(t, BaseWorldState("D2", 91), nil)
	stagedPre, _ := BaseWorldState("D2", 91).Plans()
	pre, err := rtPre.Ex.Run(stagedPre, plan.TreatP2, "pre", 0)
	if err != nil || !pre.TerminalOK {
		t.Fatalf("pre-run failed: %v", err)
	}
	oldLint := rtPre.Ex.Bodies[storeArtifact(t, rtPre, stagedPre, "lint")]
	newLint := rt.Ex.Bodies[storeArtifact(t, rt, stagedWithLintV2(t), "lint")]
	if string(oldLint) == string(newLint) {
		t.Fatal("HARD GATE FAILED: old-dependency bytes satisfy new-state node")
	}
	t.Logf("spent=%d wasted=%d assessments=%d", rep.SpentUnits, rep.WastedUnits, rep.Assessments)
}

// storeArtifact resolves a node's current artifact bytes from a runtime.
func storeArtifact(t *testing.T, rt *Runtime, p plan.PhysicalPlan, node string) string {
	t.Helper()
	rep, err := rt.Ex.Run(p, plan.TreatP2, "resolve", 0)
	if err != nil {
		t.Fatal(err)
	}
	kd, ok := rep.NodeKeys[node]
	if !ok {
		t.Fatalf("no key for %s", node)
	}
	art, ok := rt.Ex.Store.Lookup(kd)
	if !ok {
		t.Fatalf("no artifact for %s", node)
	}
	return art.ArtifactDigest
}

func stagedWithLintV2(t *testing.T) plan.PhysicalPlan {
	t.Helper()
	ws := BaseWorldState("D2", 91)
	ws.LintCfg = "lint-v2"
	staged, _ := ws.Plans()
	return staged
}

// TestMidRunLintSwitch: staged start; lint trigger + direct cost drop after
// compile → switch to direct reusing identity; final == final-world P3 oracle;
// R1-frozen continues staged (more work); R2 waste accounts compile.
func TestMidRunLintSwitch(t *testing.T) {
	sched := []ScheduledTrigger{
		lintTrigger("compile"),
		{AfterNode: "compile", Event: Event{ID: "direct-cheap", Kind: TrigCostChanged,
			Node: "direct", NewCost: 5}},
	}
	rt := testRuntime(t, freshD2(92, 40), sched)
	rep, err := rt.Run("switch")
	if err != nil {
		t.Fatalf("R2 failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("terminal rejected")
	}
	if len(rep.Switches) != 1 {
		t.Fatalf("expected exactly 1 switch, got %+v", rep.Switches)
	}
	sw := rep.Switches[0]
	if sw.OldPlan != "d2-staged" || sw.NewPlan != "d2-direct" {
		t.Fatalf("wrong switch: %+v", sw)
	}
	// Final-world oracle (P3 on final world must also pick direct).
	wsFinal := freshD2(92, 5)
	wsFinal.LintCfg = "lint-v2"
	wantPlan, want := oracleFinal(t, wsFinal)
	if wantPlan != "d2-direct" || rep.FinalBytes != want {
		t.Fatalf("final %s != oracle %s (%s)", rep.FinalBytes, want, wantPlan)
	}
	// R1 frozen: continues staged, more total work, same shape final.
	rt1 := testRuntime(t, freshD2(92, 40), sched)
	rt1.Frozen = true
	rep1, err := rt1.Run("frozen")
	if err != nil {
		t.Fatalf("R1 failed: %v", err)
	}
	if len(rt1.WouldSwitch) != 1 {
		t.Fatalf("R1 should record 1 would-switch: %+v", rt1.WouldSwitch)
	}
	if !rep1.TerminalOK {
		t.Fatal("R1 terminal rejected")
	}
	t.Logf("R2 spent=%d wasted=%d | R1 spent=%d (frozen %s)",
		rep.SpentUnits, rep.WastedUnits, rep1.SpentUnits, rep1.ActivePlan)
	if rep.SpentUnits >= rep1.SpentUnits {
		t.Fatalf("R2 saved nothing: R2=%d R1=%d", rep.SpentUnits, rep1.SpentUnits)
	}
	// compile (8+1u) executed pre-switch is abandoned by direct: wasted > 0.
	if rep.WastedUnits == 0 {
		t.Fatal("expected nonzero pre-trigger waste accounting")
	}
}
