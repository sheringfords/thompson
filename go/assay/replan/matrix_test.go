package replan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// matrix_test.go: Phase 9 R0/R1/R2 comparison. R0 = P3-chosen full
// recomputation on the FINAL world (isolated oracle). R1 = P3 frozen at start
// (same trigger schedule, switches recorded but never applied). R2 = runtime
// deterministic replanning. Attribution rule: only post-trigger work avoided
// relative to R1 counts as replanning value; reuse savings are reported
// separately (wasted/reused columns). Machine output:
// testdata/replan_matrix.json.

type ReplanRow struct {
	Scenario  string `json:"scenario"`
	Workload  string `json:"workload"`
	Treatment string `json:"treatment"`
	Plan      string `json:"plan"`
	Spent     int    `json:"spent_units"`
	Wasted    int    `json:"wasted_units"`
	Switches  int    `json:"switches"`
	FinalOK   bool   `json:"final_accepted"`
	EqOracle  bool   `json:"final_eq_r0_oracle"`
	Assess    int    `json:"assessments"`
	ReplanUS  int64  `json:"replan_overhead_us"`
	NoLegal   string `json:"no_legal_suffix,omitempty"`
}

type ReplanMatrix struct {
	Rows []ReplanRow `json:"rows"`
	Note string      `json:"note"`
}

func r0Row(t *testing.T, m *ReplanMatrix, scenario, workload string, wsFinal WorldState) (string, string) {
	t.Helper()
	planID, final := oracleFinal(t, wsFinal)
	m.Rows = append(m.Rows, ReplanRow{scenario, workload, "R0", planID, -1, -1, 0, true, true, 0, 0, ""})
	return planID, final
}

func r12Row(t *testing.T, m *ReplanMatrix, scenario, workload, treat string,
	wsInit WorldState, sched []ScheduledTrigger, oracleFinal string) {
	t.Helper()
	rt := testRuntime(t, wsInit, sched)
	if treat == "R1" {
		rt.Frozen = true
	}
	rep, err := rt.Run(treat + "-" + scenario)
	if err != nil {
		if nls, ok := err.(*NoLegalSuffix); ok {
			_ = nls
			m.Rows = append(m.Rows, ReplanRow{scenario, workload, treat, rep.ActivePlan,
				rep.SpentUnits, rep.WastedUnits, len(rep.Switches), false, false,
				rep.Assessments, rep.ReplanUS, nls.Reason})
			return
		}
		t.Fatalf("%s/%s failed: %v", scenario, treat, err)
	}
	m.Rows = append(m.Rows, ReplanRow{scenario, workload, treat, rep.ActivePlan,
		rep.SpentUnits, rep.WastedUnits, len(rep.Switches), rep.TerminalOK,
		rep.FinalBytes == oracleFinal, rep.Assessments, rep.ReplanUS, ""})
}

func TestReplanMatrix(t *testing.T) {
	m := ReplanMatrix{}

	// S1 D2 lint-switch + direct-cheap.
	s1sched := func() []ScheduledTrigger {
		return []ScheduledTrigger{
			lintTrigger("compile"),
			{AfterNode: "compile", Event: Event{ID: "direct-cheap", Kind: TrigCostChanged,
				Node: "direct", NewCost: 5}},
		}
	}
	s1final := freshD2(92, 5)
	s1final.LintCfg = "lint-v2"
	_, s1oracle := r0Row(t, &m, "lint-switch", "D2", s1final)
	r12Row(t, &m, "lint-switch", "D2", "R1", freshD2(92, 40), s1sched(), s1oracle)
	r12Row(t, &m, "lint-switch", "D2", "R2", freshD2(92, 40), s1sched(), s1oracle)

	// S2 D2 arrival (staged start, direct arrival after compile).
	s2ws := freshD2(93, 40)
	probe := testRuntime(t, s2ws, nil)
	arr := arrivalForDirect(t, probe, s2ws)
	s2sched := func() []ScheduledTrigger {
		return []ScheduledTrigger{{AfterNode: "compile", Event: arr}}
	}
	_, s2oracle := r0Row(t, &m, "arrival", "D2", s2ws)
	r12Row(t, &m, "arrival", "D2", "R1", freshD2(93, 40), s2sched(), s2oracle)
	r12Row(t, &m, "arrival", "D2", "R2", freshD2(93, 40), s2sched(), s2oracle)

	// S3 D2 verify-failure rescue (R1 fails explicitly).
	s3sched := func() []ScheduledTrigger {
		return []ScheduledTrigger{{AfterNode: "compile", Event: Event{
			ID: "lint-unverifiable", Kind: TrigVerifyFailed, Op: "d2.lint"}}}
	}
	_, s3oracle := r0Row(t, &m, "verify-failure", "D2", freshD2(94, 40))
	r12Row(t, &m, "verify-failure", "D2", "R1", freshD2(94, 40), s3sched(), s3oracle)
	r12Row(t, &m, "verify-failure", "D2", "R2", freshD2(94, 40), s3sched(), s3oracle)

	// S4 D1 doc rotation mid-run (same mechanism, second workload class).
	// NOTE: doc is digest-typed: rotate with a real digest, not a label.
	docV2 := reuse.DigestBytes([]byte("doc-seed-110-v2"))
	s4sched := func() []ScheduledTrigger {
		return []ScheduledTrigger{{AfterNode: "extract", Event: Event{
			ID: "doc-rot", Kind: TrigDepInvalidated, InputName: "doc", NewDigest: docV2}}}
	}
	wsD1 := BaseWorldState("D1", 110)
	wsD1.CostOverride["direct"] = 40 // staged start (direct priced out)
	s4final := BaseWorldState("D1", 110)
	s4final.CostOverride["direct"] = 40
	s4final.Doc = docV2
	_, s4oracle := r0Row(t, &m, "doc-rotation", "D1", s4final)
	r12Row(t, &m, "doc-rotation", "D1", "R1", wsD1, s4sched(), s4oracle)
	r12Row(t, &m, "doc-rotation", "D1", "R2", wsD1, s4sched(), s4oracle)

	// S5 sunk-cost trigger (post-completion node cost spike): R2 must record
	// zero switches — same suffix as R1.
	s5sched := func() []ScheduledTrigger {
		return []ScheduledTrigger{{AfterNode: "aggregate", Event: Event{
			ID: "sunk", Kind: TrigCostChanged, Node: "aggregate", NewCost: 60}}}
	}
	_, s5oracle := r0Row(t, &m, "sunk-cost", "D2", freshD2(96, 40))
	r12Row(t, &m, "sunk-cost", "D2", "R1", freshD2(96, 40), s5sched(), s5oracle)
	r12Row(t, &m, "sunk-cost", "D2", "R2", freshD2(96, 40), s5sched(), s5oracle)

	m.Note = "R0 spent/wasted = n/a (isolated full recomputation). EqOracle compares " +
		"final bytes against the R0 P3-chosen plan: R1 rows on a different frozen shape " +
		"are EqOracle=false BY DESIGN (different computation, still terminal-ACCEPTED). " +
		"Only post-trigger avoidance vs R1 counts as replanning value."
	writeReplanReport(t, "replan_matrix.json", m)
	for _, r := range m.Rows {
		t.Logf("%s/%s/%s plan=%s spent=%d wasted=%d switches=%d ok=%v eq=%v assess=%d replan_us=%d %s",
			r.Scenario, r.Workload, r.Treatment, r.Plan, r.Spent, r.Wasted,
			r.Switches, r.FinalOK, r.EqOracle, r.Assess, r.ReplanUS, r.NoLegal)
	}
}

func writeReplanReport(t *testing.T, name string, v interface{}) {
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
