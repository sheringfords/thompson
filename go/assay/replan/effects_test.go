package replan

import (
	"errors"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// effects_test.go: Phase 8 effect-boundary adversarial tests. External effects
// are simulated by a test-visible sink guarded at op level (double-apply and
// unknown-scope applications fail the node); the runtime additionally rejects
// incompatible suffixes BEFORE execution. Both layers are asserted.

// EffectSink simulates the external world: scope -> applied key digests.
type EffectSink struct {
	Applied map[string][]string
	Unknown map[string]bool
}

func (s *EffectSink) Op(spec EffectSpec, rt *Runtime) plan.OpFunc {
	return func(n plan.PlanNode, in map[string][]byte) ([]byte, error) {
		if s.Unknown[spec.Scope] {
			return nil, errors.New("effect scope state UNKNOWN")
		}
		out, err := plan.StdOp(n, in)
		if err != nil {
			return nil, err
		}
		d := reuse.DigestBytes(out)
		for _, prev := range s.Applied[spec.Scope] {
			if prev == d {
				return out, nil // idempotent retry: same computation, no double-apply
			}
		}
		if len(s.Applied[spec.Scope]) > 0 {
			return nil, errors.New("scope " + spec.Scope + " already committed differently")
		}
		s.Applied[spec.Scope] = append(s.Applied[spec.Scope], d)
		return out, nil
	}
}

// effectPlan builds compute -> effect(scope,class) -> attest(terminal).
// pubID names the effect node (lets alternatives carry distinct ids so
// cost overrides can discriminate them); report/atttest ids carry variant.
func effectPlan(variant, scope string, class EffectClass, cost int, pubID string) plan.PhysicalPlan {
	vc := func(op string) string { return plan.ContractFor(op) }
	nodes := []planNodeSpec{
		{id: "compute", op: "rx.compute", cost: 3},
		{id: pubID, op: "rx.publish", cost: cost, ups: []string{"compute"}},
		{id: "report-" + variant, op: "rx.report-" + variant, cost: 2, ups: []string{pubID}},
		{id: "attest", op: "rx.attest", cost: 1, ups: []string{"report-" + variant}},
	}
	_ = class
	var ns []plan.PlanNode
	for _, s := range nodes {
		ns = append(ns, plan.PlanNode{NodeID: s.id, Op: s.op, OpVersion: "v1",
			Upstreams: s.ups, Executor: "rx/1.0", VerifierContract: vc(s.op),
			PolicyDigest: reuse.DigestString("p"), CostUnits: s.cost})
	}
	_ = scope
	return plan.PhysicalPlan{PlanID: "rx-" + variant, Job: plan.LogicalJob{JobID: "rx"},
		Nodes: ns, Terminal: "attest", Policy: reuse.DigestString("p")}
}

type planNodeSpec struct {
	id   string
	op   string
	cost int
	ups  []string
}

func effectRegistry(t *testing.T, sink *EffectSink, rt *Runtime, class EffectClass, variants ...string) *plan.Registry {
	t.Helper()
	r := plan.NewRegistry()
	plan.RegisterOp(r, "rx.compute")
	plan.RegisterOp(r, "rx.attest")
	r.Ops["rx.publish"] = sink.Op(EffectSpec{Class: class, Scope: "ch"}, rt)
	r.Verifiers[plan.ContractFor("rx.publish")] = plan.StdVerify
	for _, v := range variants {
		plan.RegisterOp(r, "rx.report-"+v)
	}
	return r
}

func effectRuntime(t *testing.T, class EffectClass, variant string, sched []ScheduledTrigger) (*Runtime, *EffectSink) {
	t.Helper()
	sink := &EffectSink{Applied: map[string][]string{}, Unknown: map[string]bool{}}
	s, err := openStore(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	dir := stateDir(t)
	ex := &plan.Executor{Store: s, Reg: nil, Outcomes: map[string]reuse.Outcome{},
		Bodies: map[string][]byte{}, StateDir: dir}
	rt := NewRuntime(ex, BaseWorldState("D2", 200), func(w WorldState) []plan.PhysicalPlan {
		return []plan.PhysicalPlan{effectPlan(variant, "ch", class, 4, "publish")}
	}, dir)
	rt.Effects["rx.publish"] = EffectSpec{Class: class, Scope: "ch"}
	ex.Reg = effectRegistry(t, sink, rt, class, variant)
	rt.Schedule = sched
	return rt, sink
}

// 1. Replan before any effect: pre-start cost trigger switches shape with
// zero effects committed and completes.
func TestEffectReplanBeforeEffect(t *testing.T) {
	rt, sink := effectRuntime(t, EffectIdempotent, "a", []ScheduledTrigger{{
		AfterNode: "", Event: Event{ID: "t0", Kind: TrigCostChanged, Node: "publish", NewCost: 40},
	}})
	// Second cheaper-shaped alternative appears only via Alts override below.
	// Plan b carries a DISTINCT publish node id ("publish-b", same op): the
	// trigger's CostOverride["publish"] hurts only plan a.
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		a := effectPlan("a", "ch", EffectIdempotent, 4, "publish")
		b := effectPlan("b", "ch", EffectIdempotent, 4, "publish-b")
		applyCostA(&a, w)
		return []plan.PhysicalPlan{a, b}
	}
	plan.RegisterOp(rt.Ex.Reg, "rx.report-b")
	rep, err := rt.Run("1863491984")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rep.TerminalOK {
		t.Fatal("terminal rejected")
	}
	// publish at 40u in plan a vs 4u in plan b → must switch to b pre-effect.
	if len(rep.Switches) != 1 || rep.Switches[0].NewPlan != "rx-b" {
		t.Fatalf("no pre-effect switch: %+v", rep.Switches)
	}
	if len(sink.Applied["ch"]) != 1 {
		t.Fatalf("effect applied %d times, want 1", len(sink.Applied["ch"]))
	}
}

func applyCostA(a *plan.PhysicalPlan, w WorldState) {
	for i, n := range a.Nodes {
		if n.NodeID == "publish" {
			if c, ok := w.CostOverride["publish"]; ok {
				a.Nodes[i].CostUnits = c
			}
		}
	}
}

// 2. Replan after a PURE node: cost trigger after compute switches shape;
// the pure prefix is reused, effect applies once.
func TestEffectReplanAfterPure(t *testing.T) {
	rt, sink := effectRuntime(t, EffectIdempotent, "a", []ScheduledTrigger{{
		AfterNode: "compute", Event: Event{ID: "t1", Kind: TrigCostChanged, Node: "publish", NewCost: 40},
	}})
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		a := effectPlan("a", "ch", EffectIdempotent, 4, "publish")
		b := effectPlan("b", "ch", EffectIdempotent, 4, "publish-b")
		applyCostA(&a, w)
		return []plan.PhysicalPlan{a, b}
	}
	plan.RegisterOp(rt.Ex.Reg, "rx.report-b")
	rep, err := rt.Run("after-pure")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rep.TerminalOK || len(rep.Switches) != 1 || rep.Switches[0].NewPlan != "rx-b" {
		t.Fatalf("no post-pure switch: %+v ok=%v", rep.Switches, rep.TerminalOK)
	}
	if len(sink.Applied["ch"]) != 1 {
		t.Fatalf("effect applied %d times", len(sink.Applied["ch"]))
	}
}

// 3. Replan after an idempotent effect: effect commits on scope ch (digest D)
// under plan a; then report-a is forced to re-execute (op-version bump) at a
// spiked cost. R2 switches to plan b, reusing the same effect key. Sink holds
// exactly one application (no double-apply).
func TestEffectIdempotentReuseAcrossSwitch(t *testing.T) {
	rt, sink := effectRuntime(t, EffectIdempotent, "a", []ScheduledTrigger{
		{AfterNode: "publish", Event: Event{ID: "t2a", Kind: TrigDepInvalidated,
			Node: "report-a", InputName: "op-version", NewDigest: "v2"}},
		{AfterNode: "publish", Event: Event{ID: "t2b", Kind: TrigCostChanged,
			Node: "report-a", NewCost: 50}},
	})
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		a := effectPlan("a", "ch", EffectIdempotent, 4, "publish")
		b := effectPlan("b", "ch", EffectIdempotent, 4, "publish-b")
		if c, ok := w.CostOverride["report-a"]; ok {
			for i, n := range a.Nodes {
				if n.NodeID == "report-a" {
					a.Nodes[i].CostUnits = c
				}
			}
		}
		return []plan.PhysicalPlan{a, b}
	}
	plan.RegisterOp(rt.Ex.Reg, "rx.report-b")
	rep, err := rt.Run("idem-switch")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rep.TerminalOK || len(rep.Switches) != 1 || rep.Switches[0].NewPlan != "rx-b" {
		t.Fatalf("no idempotent switch: %+v", rep.Switches)
	}
	if len(sink.Applied["ch"]) != 1 {
		t.Fatalf("idempotent effect applied %d times (double-apply!)", len(sink.Applied["ch"]))
	}
}

// 4. Replan after an irreversible effect, incompatible alternative: plan b's
// publish carries DIFFERENT effect params (different key, same scope) while
// plan a's effect already committed → b is rejected; with no other legal
// alternative the run ends in explicit NoLegalSuffix (first-class negative).
// A compatible alternative (same effect key) is selected when present.
func TestEffectIrreversibleBoundary(t *testing.T) {
	newRT := func() (*Runtime, *EffectSink) {
		return effectRuntime(t, EffectIrreversible, "a", []ScheduledTrigger{{
			AfterNode: "publish", Event: Event{ID: "t3", Kind: TrigCostChanged,
				Node: "report-a", NewCost: 50},
		}})
	}
	// Negative: b's publish has an extra input (different effect digest).
	// Triggers force report-a to re-execute (version bump) and fail it (op
	// failure): plan a becomes illegal, plan b is scope-rejected → explicit
	// NoLegalSuffix with history intact.
	rt, sink := newRT()
	rt.Schedule = []ScheduledTrigger{
		{AfterNode: "publish", Event: Event{ID: "t3a", Kind: TrigDepInvalidated,
			Node: "report-a", InputName: "op-version", NewDigest: "v2"}},
		{AfterNode: "publish", Event: Event{ID: "t3b", Kind: TrigOpFailed, Op: "rx.report-a"}},
	}
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		a := effectPlan("a", "ch", EffectIrreversible, 4, "publish")
		b := effectPlan("b", "ch", EffectIrreversible, 4, "publish-b")
		for i, n := range b.Nodes {
			if n.NodeID == "publish-b" {
				b.Nodes[i].Inputs = []reuse.Dep{{Name: "mode", Digest: reuse.DigestString("mode-x")}}
			}
		}
		if c, ok := w.CostOverride["report-a"]; ok {
			for i, n := range a.Nodes {
				if n.NodeID == "report-a" {
					a.Nodes[i].CostUnits = c
				}
			}
		}
		return []plan.PhysicalPlan{a, b}
	}
	plan.RegisterOp(rt.Ex.Reg, "rx.report-b")
	_, err := rt.Run("irrev-neg")
	var nls *NoLegalSuffix
	if !errors.As(err, &nls) {
		t.Fatalf("want NoLegalSuffix, got %v (sink=%v)", err, sink.Applied)
	}
	if len(sink.Applied["ch"]) != 1 {
		t.Fatalf("history rewritten: %v", sink.Applied)
	}
	// Positive: compatible b (same effect key) is selected and completes.
	// Same version-bump forces report-a to re-execute at spiked cost.
	rt2, sink2 := newRT()
	rt2.Schedule = append(rt2.Schedule, ScheduledTrigger{
		AfterNode: "publish", Event: Event{ID: "t3c", Kind: TrigDepInvalidated,
			Node: "report-a", InputName: "op-version", NewDigest: "v2"},
	})
	rt2.Alts = func(w WorldState) []plan.PhysicalPlan {
		a := effectPlan("a", "ch", EffectIrreversible, 4, "publish")
		b := effectPlan("b", "ch", EffectIrreversible, 4, "publish-b")
		if c, ok := w.CostOverride["report-a"]; ok {
			for i, n := range a.Nodes {
				if n.NodeID == "report-a" {
					a.Nodes[i].CostUnits = c
				}
			}
		}
		return []plan.PhysicalPlan{a, b}
	}
	plan.RegisterOp(rt2.Ex.Reg, "rx.report-b")
	rep2, err := rt2.Run("irrev-pos")
	if err != nil || !rep2.TerminalOK {
		t.Fatalf("compatible switch failed: %v", err)
	}
	if len(rep2.Switches) != 1 || rep2.Switches[0].NewPlan != "rx-b" {
		t.Fatalf("no compatible switch: %+v", rep2.Switches)
	}
	if len(sink2.Applied["ch"]) != 1 {
		t.Fatalf("effect applied %d times", len(sink2.Applied["ch"]))
	}
}

// 5. Timeout leaves an external effect UNKNOWN: scope ch is marked ambiguous
// while its effect node is still pending. The effect plan becomes illegal;
// a pure alternative avoiding the scope is selected; sink stays empty.
func TestEffectUnknownScope(t *testing.T) {
	rt, sink := effectRuntime(t, EffectIdempotent, "a", []ScheduledTrigger{{
		AfterNode: "compute", Event: Event{ID: "t4", Kind: TrigEffectUnknown, Scope: "ch"},
	}})
	// Pure alternative: same report shape without the effect node.
	pureB := func() plan.PhysicalPlan {
		vc := func(op string) string { return plan.ContractFor(op) }
		nodes := []plan.PlanNode{
			{NodeID: "compute", Op: "rx.compute", OpVersion: "v1",
				Executor: "rx/1.0", VerifierContract: vc("rx.compute"),
				PolicyDigest: reuse.DigestString("p"), CostUnits: 3},
			{NodeID: "report-b", Op: "rx.report-b", OpVersion: "v1",
				Upstreams: []string{"compute"},
				Executor:  "rx/1.0", VerifierContract: vc("rx.report-b"),
				PolicyDigest: reuse.DigestString("p"), CostUnits: 2},
			{NodeID: "attest", Op: "rx.attest", OpVersion: "v1",
				Upstreams: []string{"report-b"},
				Executor:  "rx/1.0", VerifierContract: vc("rx.attest"),
				PolicyDigest: reuse.DigestString("p"), CostUnits: 1},
		}
		return plan.PhysicalPlan{PlanID: "rx-pure", Job: plan.LogicalJob{JobID: "rx"},
			Nodes: nodes, Terminal: "attest", Policy: reuse.DigestString("p")}
	}
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		return []plan.PhysicalPlan{effectPlan("a", "ch", EffectIdempotent, 4, "publish"), pureB()}
	}
	plan.RegisterOp(rt.Ex.Reg, "rx.report-b")
	rep, err := rt.Run("unknown-scope")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !rep.TerminalOK || rep.ActivePlan != "rx-pure" {
		t.Fatalf("UNKNOWN scope not routed around: %+v", rep)
	}
	if len(sink.Applied["ch"]) != 0 {
		t.Fatalf("ambiguous effect was applied: %v", sink.Applied)
	}
}

// 6. A candidate assuming a committed effect never occurred is rejected with
// an explicit reason (extends scenario 4's negative with reason assertion).
func TestEffectAssumesAbsentRejected(t *testing.T) {
	rt, _ := effectRuntime(t, EffectIrreversible, "a", []ScheduledTrigger{
		{AfterNode: "publish", Event: Event{ID: "t5a", Kind: TrigDepInvalidated,
			Node: "report-a", InputName: "op-version", NewDigest: "v2"}},
		{AfterNode: "publish", Event: Event{ID: "t5b", Kind: TrigOpFailed, Op: "rx.report-a"}},
	})
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		a := effectPlan("a", "ch", EffectIrreversible, 4, "publish")
		b := effectPlan("b", "ch", EffectIrreversible, 4, "publish-b")
		for i, n := range b.Nodes {
			if n.NodeID == "publish-b" {
				b.Nodes[i].Inputs = []reuse.Dep{{Name: "mode", Digest: reuse.DigestString("mode-x")}}
			}
		}
		return []plan.PhysicalPlan{a, b}
	}
	plan.RegisterOp(rt.Ex.Reg, "rx.report-b")
	_, err := rt.Run("assumes-absent")
	var nls *NoLegalSuffix
	if !errors.As(err, &nls) {
		t.Fatalf("want NoLegalSuffix, got %v", err)
	}
	if !contains(nls.Reason, "already committed") {
		t.Fatalf("rejection reason hides history: %s", nls.Reason)
	}
}

// crashResume runs rt to a crash, resumes from the durable log on the same
// store, and returns both reports. setup customizes the clean comparison
// runtime (e.g. effect registries); nil keeps the standard registry.
func crashResume(t *testing.T, rt *Runtime, runID string, crashAfter int, setup func(*Runtime)) (*RuntimeReport, *RuntimeReport) {
	t.Helper()
	rt.CrashAfter = crashAfter
	_, err := rt.Run(runID)
	if err == nil {
		t.Fatal("expected crash")
	}
	resumed := Resume(rt.Ex, BaseWorldState("D2", 200), rt.Alts, rt.Schedule, rt.StateDir)
	resumed.Effects = rt.Effects
	resumed.Budget = rt.Budget
	resumed.CrashAfter = 0
	rep, err := resumed.Run(runID)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	// Re-run uninterrupted for equivalence.
	clean := testRuntime(t, BaseWorldState("D2", 200), rt.Schedule)
	clean.Alts = rt.Alts
	clean.Effects = rt.Effects
	if setup != nil {
		setup(clean)
	}
	want, err := clean.Run(runID + "-clean")
	if err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	if rep.FinalBytes != want.FinalBytes || !rep.TerminalOK {
		t.Fatalf("resumed final %s != clean %s", rep.FinalBytes, want.FinalBytes)
	}
	if len(resumed.switches) != len(want.Switches) {
		t.Fatalf("switch history diverged: resumed %+v vs clean %+v",
			resumed.switches, want.Switches)
	}
	return rep, want
}

// effectCleanSetup installs a fresh guarded effect registry on a runtime.
func effectCleanSetup(class EffectClass, variants ...string) func(*Runtime) {
	return func(rt *Runtime) {
		sink := &EffectSink{Applied: map[string][]string{}, Unknown: map[string]bool{}}
		r := plan.NewRegistry()
		plan.RegisterOp(r, "rx.compute")
		plan.RegisterOp(r, "rx.attest")
		r.Ops["rx.publish"] = sink.Op(EffectSpec{Class: class, Scope: "ch"}, rt)
		r.Verifiers[plan.ContractFor("rx.publish")] = plan.StdVerify
		for _, v := range variants {
			plan.RegisterOp(r, "rx.report-"+v)
		}
		rt.Ex.Reg = r
	}
}

// 7. Crash after effect commit but before any replan record: the committed
// effect survives, triggers replay, history converges with the clean run.
func TestEffectCrashAfterCommit(t *testing.T) {
	rt, _ := effectRuntime(t, EffectIdempotent, "a", []ScheduledTrigger{{
		AfterNode: "report-a", Event: Event{ID: "t6", Kind: TrigCostChanged,
			Node: "report-a", NewCost: 50},
	}})
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		a := effectPlan("a", "ch", EffectIdempotent, 4, "publish")
		b := effectPlan("b", "ch", EffectIdempotent, 4, "publish-b")
		if c, ok := w.CostOverride["report-a"]; ok {
			for i, n := range a.Nodes {
				if n.NodeID == "report-a" {
					a.Nodes[i].CostUnits = c
				}
			}
		}
		return []plan.PhysicalPlan{a, b}
	}
	plan.RegisterOp(rt.Ex.Reg, "rx.report-b")
	// Crash after 2 executions (compute, publish committed; trigger at
	// report-a never applied pre-crash → no replan record yet).
	rep, _ := crashResume(t, rt, "crash-commit", 2, effectCleanSetup(EffectIdempotent, "a", "b"))
	if !rep.TerminalOK {
		t.Fatal("resumed run rejected")
	}
}

// 8. Crash after the replan record but before remaining work completes:
// the switch survives; resume continues the new suffix without redoing it.
func TestEffectCrashAfterSwitch(t *testing.T) {
	rt, _ := effectRuntime(t, EffectIdempotent, "a", []ScheduledTrigger{{
		AfterNode: "publish", Event: Event{ID: "t7", Kind: TrigCostChanged,
			Node: "report-a", NewCost: 50},
	}})
	rt.Alts = func(w WorldState) []plan.PhysicalPlan {
		a := effectPlan("a", "ch", EffectIdempotent, 4, "publish")
		b := effectPlan("b", "ch", EffectIdempotent, 4, "publish-b")
		if c, ok := w.CostOverride["report-a"]; ok {
			for i, n := range a.Nodes {
				if n.NodeID == "report-a" {
					a.Nodes[i].CostUnits = c
				}
			}
		}
		// report-a must need execution for the spike to matter.
		if _, ok := w.CostOverride["report-a"]; ok {
			for i, n := range a.Nodes {
				if n.NodeID == "report-a" {
					a.Nodes[i].OpVersion = "v2"
				}
			}
		}
		return []plan.PhysicalPlan{a, b}
	}
	plan.RegisterOp(rt.Ex.Reg, "rx.report-b")
	// Crash after 4 cumulative executions (compute, publish, + first two of
	// the switched suffix): the switch record must survive.
	rep, _ := crashResume(t, rt, "crash-switch", 4, effectCleanSetup(EffectIdempotent, "a", "b"))
	if !rep.TerminalOK {
		t.Fatal("resumed run rejected")
	}
	if len(rep.Switches) != 1 || rep.Switches[0].NewPlan != "rx-b" {
		t.Fatalf("switch lost across crash: %+v", rep.Switches)
	}
}
