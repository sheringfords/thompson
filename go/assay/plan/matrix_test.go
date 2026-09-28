package plan

import (
	"testing"
)

// matrix_test.go: Phase 4 consolidated treatment matrix (P0/P1/P2/P3 x
// cold/warm/mutations x D1/D2) with machine-readable output.
// testdata/plan_matrix.json. Isolation rule: every cold row runs on a FRESH
// executor+store; warm/mutation rows pre-warm explicitly. P3 finals compare
// against the oracle of the CHOSEN plan (cross-plan byte equality is not
// expected — different computations).

type MatrixRow struct {
	Workload  string `json:"workload"`
	Scenario  string `json:"scenario"`
	Treatment string `json:"treatment"`
	Plan      string `json:"plan"`
	Executed  int    `json:"executed"`
	Reused    int    `json:"reused"`
	Skipped   int    `json:"skipped"`
	FinalOK   bool   `json:"final_accepted"`
	FinalEqP0 bool   `json:"final_eq_same_plan_p0"`
	WorkUnits int    `json:"exec_work_units"`
}

type MatrixReport struct {
	Rows []MatrixRow `json:"rows"`
}

func workUnits(plan PhysicalPlan, executed []string) int {
	byID := map[string]PlanNode{}
	for _, n := range plan.Nodes {
		byID[n.NodeID] = n
	}
	s := 0
	for _, id := range executed {
		s += byID[id].CostUnits
	}
	return s
}

// coldRow runs treatment on a fresh store.
func coldRow(t *testing.T, rep *MatrixReport, workload, scenario, treat string, plan PhysicalPlan) {
	t.Helper()
	e := testExecutor(t)
	r := runTreat(t, e, plan, treat, "cold")
	want, ok := oracleFinal(t, plan)
	if !ok {
		t.Fatalf("%s/%s/%s oracle rejected", workload, scenario, treat)
	}
	if !r.TerminalOK || r.Final != want {
		t.Fatalf("%s/%s/%s final mismatch", workload, scenario, treat)
	}
	rep.Rows = append(rep.Rows, MatrixRow{workload, scenario, treat, plan.PlanID,
		len(r.Executed), len(r.Reused), len(r.Skipped), r.TerminalOK, true,
		workUnits(plan, r.Executed)})
}

// warmRow pre-warms with a P2 cold run, then runs treatment.
func warmRow(t *testing.T, rep *MatrixReport, workload, scenario, treat string, plan PhysicalPlan) {
	t.Helper()
	e := testExecutor(t)
	runTreat(t, e, plan, TreatP2, "warmup")
	r := runTreat(t, e, plan, treat, "warm")
	want, ok := oracleFinal(t, plan)
	if !ok {
		t.Fatalf("%s/%s/%s oracle rejected", workload, scenario, treat)
	}
	if !r.TerminalOK || r.Final != want {
		t.Fatalf("%s/%s/%s final mismatch", workload, scenario, treat)
	}
	rep.Rows = append(rep.Rows, MatrixRow{workload, scenario, treat, plan.PlanID,
		len(r.Executed), len(r.Reused), len(r.Skipped), r.TerminalOK, true,
		workUnits(plan, r.Executed)})
}

// p3Row runs P3 over alternatives on a store prepared by prep, comparing
// against the chosen plan's oracle.
func p3Row(t *testing.T, rep *MatrixReport, workload, scenario string,
	prep func(e *Executor), alts []PhysicalPlan) {
	t.Helper()
	e := testExecutor(t)
	if prep != nil {
		prep(e)
	}
	r3, quote, err := e.RunP3(alts, "p3", 0)
	if err != nil {
		t.Fatalf("%s/%s P3 failed: %v", workload, scenario, err)
	}
	var chosen PhysicalPlan
	for _, p := range alts {
		if p.PlanID == r3.PlanID {
			chosen = p
		}
	}
	want, ok := oracleFinal(t, chosen)
	if !ok {
		t.Fatalf("%s/%s chosen-plan oracle rejected", workload, scenario)
	}
	if !r3.TerminalOK || r3.FinalBytes != want {
		t.Fatalf("%s/%s P3 final mismatch (chose %s): %s", workload, scenario, r3.PlanID, quote.Reason)
	}
	rep.Rows = append(rep.Rows, MatrixRow{workload, scenario, TreatP3, r3.PlanID,
		len(r3.Executed), len(r3.Reused), len(r3.Skipped), r3.TerminalOK, true,
		workUnits(chosen, r3.Executed)})
	t.Logf("%s/%s P3 chose %s (%s)", workload, scenario, r3.PlanID, quote.Reason)
}

func TestTreatmentMatrix(t *testing.T) {
	rep := MatrixReport{}

	// D1 staged cold/warm; P3 cold (expect direct), warm (expect staged),
	// summary-op mutation (expect direct at 0).
	staged81, direct81 := buildD1(81, d1Doc(81))
	for _, tr := range []string{TreatP0, TreatP1, TreatP2} {
		coldRow(t, &rep, "D1", "cold", tr, staged81)
	}
	for _, tr := range []string{TreatP1, TreatP2} {
		warmRow(t, &rep, "D1", "warm", tr, staged81)
	}
	p3Row(t, &rep, "D1", "cold", nil, []PhysicalPlan{staged81, direct81})
	p3Row(t, &rep, "D1", "warm", func(e *Executor) {
		runTreat(t, e, staged81, TreatP2, "warmup")
	}, []PhysicalPlan{staged81, direct81})
	mut81, mutd81 := buildD1(81, d1Doc(81))
	for i, n := range mut81.Nodes {
		if n.NodeID == "summary" {
			mut81.Nodes[i].OpVersion = "v2"
		}
	}
	for _, tr := range []string{TreatP1, TreatP2} {
		func() {
			e := testExecutor(t)
			runTreat(t, e, staged81, TreatP2, "warmup")
			r := runTreat(t, e, mut81, tr, "mut")
			want, ok := oracleFinal(t, mut81)
			if !ok || !r.TerminalOK || r.Final != want {
				t.Fatalf("D1/summary-op/%s mismatch", tr)
			}
			rep.Rows = append(rep.Rows, MatrixRow{"D1", "summary-op-change", tr, mut81.PlanID,
				len(r.Executed), len(r.Reused), len(r.Skipped), true, true,
				workUnits(mut81, r.Executed)})
		}()
	}
	p3Row(t, &rep, "D1", "summary-op-change", func(e *Executor) {
		runTreat(t, e, staged81, TreatP2, "warmup")
	}, []PhysicalPlan{mut81, mutd81})

	// D2 staged cold/warm; lintcfg branch; tree root.
	staged82, direct82 := buildD2(82, d2Tree(82), "go1.22.0", "lint-v1")
	for _, tr := range []string{TreatP0, TreatP1, TreatP2} {
		coldRow(t, &rep, "D2", "cold", tr, staged82)
	}
	for _, tr := range []string{TreatP1, TreatP2} {
		warmRow(t, &rep, "D2", "warm", tr, staged82)
	}
	p3Row(t, &rep, "D2", "cold", nil, []PhysicalPlan{staged82, direct82})
	p3Row(t, &rep, "D2", "warm", func(e *Executor) {
		runTreat(t, e, staged82, TreatP2, "warmup")
	}, []PhysicalPlan{staged82, direct82})
	lintmut, _ := buildD2(82, d2Tree(82), "go1.22.0", "lint-v2")
	for _, tr := range []string{TreatP1, TreatP2} {
		func() {
			e := testExecutor(t)
			runTreat(t, e, staged82, TreatP2, "warmup")
			r := runTreat(t, e, lintmut, tr, "mut")
			want, ok := oracleFinal(t, lintmut)
			if !ok || !r.TerminalOK || r.Final != want {
				t.Fatalf("D2/lintcfg/%s mismatch", tr)
			}
			rep.Rows = append(rep.Rows, MatrixRow{"D2", "lintcfg-change", tr, lintmut.PlanID,
				len(r.Executed), len(r.Reused), len(r.Skipped), true, true,
				workUnits(lintmut, r.Executed)})
		}()
	}
	treemut, _ := buildD2(83, d2Tree(83), "go1.22.0", "lint-v1")
	for _, tr := range []string{TreatP1, TreatP2} {
		coldRow(t, &rep, "D2", "tree-change", tr, treemut)
	}
	writePlanReport(t, "plan_matrix.json", rep)
	for _, r := range rep.Rows {
		t.Logf("%s/%s/%s plan=%s exec=%d reused=%d skipped=%d units=%d ok=%v eqP0=%v",
			r.Workload, r.Scenario, r.Treatment, r.Plan, r.Executed, r.Reused,
			r.Skipped, r.WorkUnits, r.FinalOK, r.FinalEqP0)
	}
}
