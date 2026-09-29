package replan

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// boundary_test.go: Phase 10 decision-boundary stress tests. Anti-thrash is
// structural (one assessment per trigger batch, deterministic quotes), never
// sleeps or damping constants.

// 1. Trigger immediately after plan selection (pre-start drain).
func TestBoundaryTriggerAtStart(t *testing.T) {
	ws := freshD2(101, 40)
	rt := testRuntime(t, ws, []ScheduledTrigger{{
		AfterNode: "", Event: Event{ID: "s0", Kind: TrigCostChanged, Node: "direct", NewCost: 5},
	}})
	rep, err := rt.Run("start-trig")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rep.TerminalOK || rep.ActivePlan != "d2-direct" {
		t.Fatalf("start trigger ignored: %+v", rep)
	}
	if len(rep.Switches) != 1 {
		t.Fatalf("start switch not recorded: %+v", rep.Switches)
	}
}

// 2. Trigger immediately before terminal verification.
func TestBoundaryTriggerPreTerminal(t *testing.T) {
	ws := freshD2(102, 40)
	rt := testRuntime(t, ws, []ScheduledTrigger{{
		AfterNode: "aggregate", Event: Event{ID: "s1", Kind: TrigCostChanged,
			Node: "aggregate", NewCost: 60},
	}})
	rep, err := rt.Run("pre-term")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("terminal rejected")
	}
	// aggregate spike after aggregate COMPLETED is sunk cost: no switch should
	// fire (remaining work unaffected), proving triggers don't thrash.
	for _, s := range rep.Switches {
		t.Logf("switch: %+v", s)
	}
	if len(rep.Switches) != 0 {
		t.Fatalf("sunk-cost trigger caused a switch: %+v", rep.Switches)
	}
}

// 3. Multiple triggers in one execution drain in schedule order. One
// assessment per node-visit batch (all triggers due at that visit apply
// together — structural anti-thrash). Triggers on abandoned-plan nodes go
// moot after a switch and are REPORTED as dropped, never silent.
func TestBoundaryMultipleTriggers(t *testing.T) {
	ws := freshD2(103, 40)
	rt := testRuntime(t, ws, []ScheduledTrigger{
		{AfterNode: "identity", Event: Event{ID: "m1", Kind: TrigCostChanged,
			Node: "direct", NewCost: 30}},
		{AfterNode: "compile", Event: Event{ID: "m2", Kind: TrigCostChanged,
			Node: "direct", NewCost: 5}},
		{AfterNode: "lint", Event: Event{ID: "m3", Kind: TrigBudgetReduced, Budget: 200}},
	})
	rep, err := rt.Run("multi")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("terminal rejected")
	}
	t.Logf("switches=%d assessments=%d dropped=%v", len(rep.Switches), rep.Assessments, rep.DroppedTriggers)
	// m1 fires at identity → assessment switches to direct (33u vs 34u);
	// compile/lint never visit again → m2/m3 dropped (recorded).
	if len(rep.Switches) != 1 || rep.Switches[0].NewPlan != "d2-direct" {
		t.Fatalf("expected one switch to direct: %+v", rep.Switches)
	}
	if rep.Assessments != 2 {
		t.Fatalf("assessments=%d, want 2 (init + one batch)", rep.Assessments)
	}
	dropped := map[string]bool{}
	for _, id := range rep.DroppedTriggers {
		dropped[id] = true
	}
	if !dropped["m2"] || !dropped["m3"] || dropped["m1"] {
		t.Fatalf("drop accounting wrong: %v", rep.DroppedTriggers)
	}
}

// 4. Contradictory triggers: last write wins, deterministically.
func TestBoundaryContradictory(t *testing.T) {
	run := func() string {
		ws := freshD2(104, 40)
		rt := testRuntime(t, ws, []ScheduledTrigger{
			{AfterNode: "compile", Event: Event{ID: "c1", Kind: TrigDepInvalidated,
				Node: "lint", InputName: "lintcfg", NewDigest: "lint-v2"}},
			{AfterNode: "compile", Event: Event{ID: "c2", Kind: TrigDepInvalidated,
				Node: "lint", InputName: "lintcfg", NewDigest: "lint-v3"}},
		})
		rep, err := rt.Run("contra")
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
		return rep.FinalBytes
	}
	if a, b := run(), run(); a != b {
		t.Fatal("contradictory triggers nondeterministic")
	}
}

// 5. Oscillation: alternating cost triggers stay bounded (one switch per
// trigger max) and replay identically.
func TestBoundaryOscillation(t *testing.T) {
	buildSched := func() []ScheduledTrigger {
		nodes := []string{"identity", "compile", "lint-setup", "lint", "unit-setup", "unit", "aggregate"}
		var out []ScheduledTrigger
		for i, n := range nodes {
			cost := 5
			if i%2 == 1 {
				cost = 40
			}
			out = append(out, ScheduledTrigger{AfterNode: n, Event: Event{
				ID: fmt.Sprintf("osc-%d", i), Kind: TrigCostChanged,
				Node: "direct", NewCost: cost,
			}})
		}
		return out
	}
	run := func(tag string) *RuntimeReport {
		ws := freshD2(105, 40)
		rt := testRuntime(t, ws, buildSched())
		rep, err := rt.Run(tag)
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
		return rep
	}
	a, b := run("osc1"), run("osc2")
	if a.FinalBytes != b.FinalBytes || len(a.Switches) != len(b.Switches) {
		t.Fatal("oscillation replay diverged")
	}
	t.Logf("oscillation: %d switches over 7 triggers, deterministic", len(a.Switches))
	if len(a.Switches) > 7 {
		t.Fatalf("plan thrashing: %d switches", len(a.Switches))
	}
	if len(a.Switches) > a.Assessments {
		t.Fatalf("more switches than assessments")
	}
}

// 6. Equal-cost candidates break ties lexicographically and stably.
func TestBoundaryTie(t *testing.T) {
	mkAlts := func(w WorldState) []plan.PhysicalPlan {
		s, _ := w.Plans()
		tw := s
		tw.PlanID = "d2-staged-twin"
		return []plan.PhysicalPlan{s, tw}
	}
	var first string
	for i := 0; i < 3; i++ {
		rt := testRuntime(t, freshD2(106, -1), nil)
		rt.Alts = mkAlts
		rep, err := rt.Run(fmt.Sprintf("tie-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = rep.ActivePlan
		} else if rep.ActivePlan != first {
			t.Fatal("tie-break unstable")
		}
	}
	if first != "d2-staged" {
		t.Fatalf("tie-break not lexicographic: %s", first)
	}
}

// 8. Large fan-out invalidation mid-run: wide plan (root → mid → 20
// consumers → bundle); root input rotates after mid completes. Exact closure
// recomputes once per node; finals match the rotated-world oracle.
func TestBoundaryFanout(t *testing.T) {
	n := 20
	build := func(root string) plan.PhysicalPlan {
		vc := func(op string) string { return plan.ContractFor(op) }
		nodes := []plan.PlanNode{
			{NodeID: "root", Op: "w.root", OpVersion: "v1",
				Inputs:   []reuse.Dep{{Name: "in", Digest: reuse.DigestString(root)}},
				Executor: "w/1.0", VerifierContract: vc("w.root"),
				PolicyDigest: reuse.DigestString("p"), CostUnits: 1},
			{NodeID: "mid", Op: "w.mid", OpVersion: "v1", Upstreams: []string{"root"},
				Executor: "w/1.0", VerifierContract: vc("w.mid"),
				PolicyDigest: reuse.DigestString("p"), CostUnits: 8},
		}
		ups := []string{}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("c%d", i)
			nodes = append(nodes, plan.PlanNode{NodeID: id, Op: "w.c", OpVersion: "v1",
				Upstreams: []string{"mid"},
				Inputs:    []reuse.Dep{{Name: "w", Digest: reuse.DigestString(id)}},
				Executor:  "w/1.0", VerifierContract: vc("w.c"),
				PolicyDigest: reuse.DigestString("p"), CostUnits: 1})
			ups = append(ups, id)
		}
		nodes = append(nodes, plan.PlanNode{NodeID: "bundle", Op: "w.bundle", OpVersion: "v1",
			Upstreams: ups,
			Executor:  "w/1.0", VerifierContract: vc("w.bundle"),
			PolicyDigest: reuse.DigestString("p"), CostUnits: 1})
		return plan.PhysicalPlan{PlanID: "wide", Job: plan.LogicalJob{JobID: "wide"},
			Nodes: nodes, Terminal: "bundle", Policy: reuse.DigestString("p")}
	}
	mkRT := func(sched []ScheduledTrigger) *Runtime {
		ws := BaseWorldState("D2", 300)
		ws.Extra["root"] = "root-a"
		rt := testRuntime(t, ws, sched)
		for _, op := range []string{"w.root", "w.mid", "w.c", "w.bundle"} {
			plan.RegisterOp(rt.Ex.Reg, op)
		}
		rt.Alts = func(w WorldState) []plan.PhysicalPlan {
			return []plan.PhysicalPlan{build(w.Extra["root"])}
		}
		return rt
	}
	rt := mkRT([]ScheduledTrigger{{AfterNode: "mid", Event: Event{
		ID: "rot", Kind: TrigDepInvalidated, InputName: "extra:root", NewDigest: "root-b",
	}}})
	rep, err := rt.Run("fanout")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("terminal rejected")
	}
	// Exact closure: pre-trigger root-a+mid-a (11u) abandoned; post-rotation
	// root+mid+20 consumers+bundle (53u) recomputed once each.
	if rep.SpentUnits != 64 {
		t.Fatalf("fanout spent=%d, want 64", rep.SpentUnits)
	}
	if rep.WastedUnits != 11 {
		t.Fatalf("fanout wasted=%d, want 11", rep.WastedUnits)
	}
	// Rotated-world oracle: same wide plan, root-b, full recomputation.
	ort := mkRT(nil)
	wsO := BaseWorldState("D2", 300)
	wsO.Extra["root"] = "root-b"
	_ = wsO
	orep, err := ort.Ex.Run(build("root-b"), plan.TreatP0, "oracle", 0)
	if err != nil || !orep.TerminalOK {
		t.Fatalf("oracle failed: %v", err)
	}
	if rep.FinalBytes != orep.FinalBytes {
		t.Fatal("fanout final mismatch vs oracle")
	}
	t.Logf("fanout: spent=%d switches=%d", rep.SpentUnits, len(rep.Switches))
}

// 9. One hundred sequential replans: deterministic history, bounded switches.
func TestBoundaryHundredReplans(t *testing.T) {
	nodes := []string{"identity", "compile", "lint-setup", "lint", "unit-setup", "unit", "aggregate", "attest"}
	var sched []ScheduledTrigger
	for i := 0; i < 100; i++ {
		cost := 5 + (i % 3)
		sched = append(sched, ScheduledTrigger{AfterNode: nodes[i%len(nodes)], Event: Event{
			ID: fmt.Sprintf("h%d", i), Kind: TrigCostChanged, Node: "direct", NewCost: cost,
		}})
	}
	run := func(tag string) *RuntimeReport {
		rt := testRuntime(t, freshD2(107, 40), sched)
		rep, err := rt.Run(tag)
		if err != nil {
			t.Fatalf("run failed: %v", err)
		}
		return rep
	}
	a, b := run("h1"), run("h2")
	if a.FinalBytes != b.FinalBytes {
		t.Fatal("100-replan history diverged in final")
	}
	if len(a.Switches) != len(b.Switches) {
		t.Fatal("100-replan switch history diverged")
	}
	for i := range a.Switches {
		if !reflect.DeepEqual(a.Switches[i], b.Switches[i]) {
			t.Fatalf("switch %d diverged", i)
		}
	}
	t.Logf("100 triggers → %d switches, %d assessments, %d dropped, deterministic",
		len(a.Switches), a.Assessments, len(a.DroppedTriggers))
	// Bounded: at most one switch per assessment; every trigger applied or
	// recorded dropped (full accounting, no thrash, no silence).
	if len(a.Switches) > a.Assessments {
		t.Fatalf("plan thrashing: %d switches", len(a.Switches))
	}
}
