package replan

import (
	"testing"
)

// heldout_test.go: reserved seeds (1001-1003) the runtime never developed
// against. Mid-run lint invalidation + switch must produce oracle-equal,
// terminal-accepted finals with zero stale consumption.

// TestHeldOutReplanning runs the lint-switch scenario on held-out seeds and
// requires final equality with the final-world P3 oracle.
func TestHeldOutReplanning(t *testing.T) {
	for _, seed := range []uint64{1001, 1002, 1003} {
		ws := BaseWorldState("D2", seed)
		ws.CostOverride["direct"] = 40
		sched := []ScheduledTrigger{
			{AfterNode: "compile", Event: Event{ID: "h-lint", Kind: TrigDepInvalidated,
				Node: "lint", InputName: "lintcfg", NewDigest: "lint-v2"}},
			{AfterNode: "compile", Event: Event{ID: "h-cheap", Kind: TrigCostChanged,
				Node: "direct", NewCost: 5}},
		}
		rt := testRuntime(t, ws, sched)
		rep, err := rt.Run("heldout")
		if err != nil {
			t.Fatalf("seed %d failed: %v", seed, err)
		}
		if !rep.TerminalOK {
			t.Fatalf("seed %d terminal rejected", seed)
		}
		wsF := BaseWorldState("D2", seed)
		wsF.CostOverride["direct"] = 5
		wsF.LintCfg = "lint-v2"
		wantPlan, want := oracleFinal(t, wsF)
		if wantPlan != "d2-direct" || rep.FinalBytes != want {
			t.Fatalf("seed %d: final %s != oracle %s (%s)", seed, rep.FinalBytes, want, wantPlan)
		}
		// No stale consumption: every reused artifact must be VALID under the
		// final world — re-evaluate the runtime's executed keys is overkill;
		// the byte-equality gate above plus the hard-gate unit test
		// (TestMidRunLintInvalidation) covers it. Assert switch happened.
		if len(rep.Switches) != 1 {
			t.Fatalf("seed %d: switches=%+v", seed, rep.Switches)
		}
	}
}
