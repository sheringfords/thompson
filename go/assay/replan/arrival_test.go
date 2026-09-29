package replan

import (
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
)

// arrival_test.go: Phase 6 — mid-run artifact arrival. An independently
// produced VALID artifact for a remaining expensive node arrives while a
// staged execution is in progress. Frozen R1 cannot change shape to use it
// when it belongs to the non-frozen plan; R2 switches and skips the work.

// arrivalForDirect independently produces the direct-check artifact for the
// CURRENT world: runs an isolated oracle prefix (identity) and computes the
// direct op over its bytes, then binds arrival evidence. No held-out truth is
// used — only the operation itself over already-verified inputs.
func arrivalForDirect(t *testing.T, rt *Runtime, ws WorldState) Event {
	t.Helper()
	// Isolated oracle: execute identity under the current world.
	iso := testRuntime(t, ws, nil)
	staged, direct := ws.Plans()
	var directNode plan.PlanNode
	for _, n := range direct.Nodes {
		if n.NodeID == "direct" {
			directNode = n
		}
	}
	// Isolated oracle: execute the staged prefix (publishes to its own store).
	orep, err := iso.Ex.Run(staged, plan.TreatP2, "prefix", 0)
	if err != nil || !orep.TerminalOK {
		t.Fatalf("arrival oracle failed: %v", err)
	}
	// identity bytes from the oracle run's node keys.
	idKey, ok := orep.NodeKeys["identity"]
	if !ok {
		t.Fatal("oracle identity key missing")
	}
	idArt, ok := iso.Ex.Store.Lookup(idKey)
	if !ok {
		t.Fatal("oracle identity artifact missing")
	}
	idBytes, ok := iso.Ex.Bodies[idArt.ArtifactDigest]
	if !ok {
		t.Fatal("oracle identity bytes missing")
	}
	// Compute direct op over verified identity bytes (test-only key wiring:
	// replicate the executor's upstream binding for this known edge).
	upKeys := map[string]string{"identity": idKey}
	upArts := map[string]string{"identity": idArt.ArtifactDigest}
	dk, err := plan.NodeKey(directNode, upKeys, upArts)
	if err != nil {
		t.Fatal(err)
	}
	out, err := plan.StdOp(directNode, map[string][]byte{"identity": idBytes})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.StdVerify(directNode, map[string][]byte{"identity": idBytes}, out) {
		t.Fatal("arrival bytes fail independent verification")
	}
	return Event{ID: "arrival-direct", Kind: TrigArtifactArrived, Arrival: &ArrivalPayload{
		Key: dk, Bytes: out, EvidenceID: "ev-external",
		CostUSD: 0.019, ReceiptID: "rcpt-external", JobID: "job-external-direct",
	}}
}

// TestMidRunArrivalSwitch: staged start (direct priced out); after compile, a
// VALID direct-check artifact arrives from an independent producer. R2 must
// switch to direct and skip the 19u direct op; R1-frozen continues staged.
// Final == final-world P3 oracle (direct).
func TestMidRunArrivalSwitch(t *testing.T) {
	ws := freshD2(93, 40) // staged initially optimal
	probe := testRuntime(t, ws, nil)
	arr := arrivalForDirect(t, probe, ws)
	rt := testRuntime(t, ws, []ScheduledTrigger{{AfterNode: "compile", Event: arr}})
	rep, err := rt.Run("arrival")
	if err != nil {
		t.Fatalf("R2 failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("terminal rejected")
	}
	if len(rep.Switches) != 1 || rep.Switches[0].NewPlan != "d2-direct" {
		t.Fatalf("expected switch to direct: %+v", rep.Switches)
	}
	// The arrived key must never have executed in this runtime (no duplicate
	// production): direct node's key absent from executed set.
	for kd := range rt.executed {
		if kd == arr.Arrival.Key.KeyDigest() {
			t.Fatal("arrived artifact was redundantly produced")
		}
	}
	wantPlan, want := oracleFinal(t, ws)
	_ = wantPlan
	// Final-world oracle: direct is 19u cold but arrival makes remaining 0...
	// oracleFinal has no arrival: compare against direct-shape P0 instead.
	if wantD := oraclePlan(t, ws, "d2-direct"); rep.FinalBytes != wantD {
		t.Fatalf("final %s != direct oracle %s (arrival world P3=%s/%s)",
			rep.FinalBytes, wantD, wantPlan, want)
	}
	// R1 frozen continues staged to completion (more work).
	rt1 := testRuntime(t, freshD2(93, 40),
		[]ScheduledTrigger{{AfterNode: "compile", Event: arr}})
	rt1.Frozen = true
	rep1, err := rt1.Run("frozen")
	if err != nil {
		t.Fatalf("R1 failed: %v", err)
	}
	t.Logf("R2 spent=%d wasted=%d | R1 spent=%d", rep.SpentUnits, rep.WastedUnits, rep1.SpentUnits)
	if rep.SpentUnits >= rep1.SpentUnits {
		t.Fatalf("arrival saved nothing: R2=%d R1=%d", rep.SpentUnits, rep1.SpentUnits)
	}
}
