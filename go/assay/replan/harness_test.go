package replan

import (
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

var d1Ops = []string{"d1.validate", "d1.extract", "d1.normalize", "d1.aggregate",
	"d1.report", "d1.attest", "d1.summary", "d1.direct"}

var d2Ops = []string{"d2.identity", "d2.compile", "d2.setup", "d2.unit",
	"d2.lint", "d2.aggregate", "d2.attest", "d2.direct"}

func openStore(t *testing.T) (*reuse.Store, error) {
	t.Helper()
	return reuse.Open(filepath.Join(t.TempDir(), "replan.jsonl"))
}

func newExecutor(t *testing.T, s *reuse.Store) *plan.Executor {
	t.Helper()
	return &plan.Executor{Store: s, Reg: testRegistry(), Outcomes: map[string]reuse.Outcome{},
		Bodies: map[string][]byte{}, StateDir: t.TempDir()}
}

func stateDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func testRegistry() *plan.Registry {
	r := plan.NewRegistry()
	for _, op := range append(append([]string{}, d1Ops...), d2Ops...) {
		plan.RegisterOp(r, op)
	}
	return r
}

func testRuntime(t *testing.T, ws WorldState, schedule []ScheduledTrigger) *Runtime {
	t.Helper()
	s, err := reuse.Open(filepath.Join(t.TempDir(), "replan.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	dir := t.TempDir()
	ex := &plan.Executor{Store: s, Reg: testRegistry(), Outcomes: map[string]reuse.Outcome{},
		Bodies: map[string][]byte{}, StateDir: dir}
	rt := NewRuntime(ex, ws, func(w WorldState) []plan.PhysicalPlan {
		staged, direct := w.Plans()
		return []plan.PhysicalPlan{staged, direct}
	}, dir)
	rt.Schedule = schedule
	return rt
}

// oracleFinal runs full recomputation of the P3-chosen plan on the FINAL world
// (isolated store): ground truth for R2 comparisons.
func oracleFinal(t *testing.T, ws WorldState) (planID, final string) {
	t.Helper()
	s, err := reuse.Open(filepath.Join(t.TempDir(), "oracle.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ex := &plan.Executor{Store: s, Reg: testRegistry(), Outcomes: map[string]reuse.Outcome{},
		Bodies: map[string][]byte{}, StateDir: t.TempDir()}
	staged, direct := ws.Plans()
	chosen, _, err := ex.ChoosePlan([]plan.PhysicalPlan{staged, direct})
	if err != nil {
		t.Fatalf("oracle choose: %v", err)
	}
	rep, err := ex.Run(chosen, plan.TreatP0, "oracle", 0)
	if err != nil || !rep.TerminalOK {
		t.Fatalf("oracle failed: %v ok=%v", err, rep.TerminalOK)
	}
	return chosen.PlanID, rep.FinalBytes
}

// frozenFinal runs the P3 choice frozen at job start against the live trigger
// schedule (R1): assessments run but switches never apply.
func frozenRuntime(t *testing.T, ws WorldState, schedule []ScheduledTrigger) (*Runtime, *RuntimeReport, error) {
	t.Helper()
	rt := testRuntime(t, ws, schedule)
	rt.Frozen = true
	rep, err := rt.Run("frozen")
	return rt, rep, err
}
