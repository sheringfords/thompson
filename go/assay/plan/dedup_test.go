package plan

import (
	"fmt"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// dedup_test.go: Phase 6 shared-subgraph deduplication assay.

// fanPlan builds root -> mid -> {c1..cn} -> bundle(terminal): one expensive
// intermediate feeding n consumers.
func fanPlan(n int, rootDigest string) PhysicalPlan {
	vc := func(op string) string { return contractFor(op) }
	nodes := []PlanNode{
		{NodeID: "root", Op: "f.root", OpVersion: "v1",
			Inputs:   []reuse.Dep{{Name: "in", Digest: rootDigest}},
			Executor: "f/1.0", VerifierContract: vc("f.root"),
			PolicyDigest: reuse.DigestString("p"), CostUnits: 2},
		{NodeID: "mid", Op: "f.mid", OpVersion: "v1", Upstreams: []string{"root"},
			Executor: "f/1.0", VerifierContract: vc("f.mid"),
			PolicyDigest: reuse.DigestString("p"), CostUnits: 10},
	}
	ups := []string{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		nodes = append(nodes, PlanNode{NodeID: id, Op: "f.consumer", OpVersion: "v1",
			Upstreams: []string{"mid"},
			Inputs:    []reuse.Dep{{Name: "which", Digest: reuse.DigestString(id)}},
			Executor:  "f/1.0", VerifierContract: vc("f.consumer"),
			PolicyDigest: reuse.DigestString("p"), CostUnits: 2})
		ups = append(ups, id)
	}
	nodes = append(nodes, PlanNode{NodeID: "bundle", Op: "f.bundle", OpVersion: "v1",
		Upstreams: ups,
		Executor:  "f/1.0", VerifierContract: vc("f.bundle"),
		PolicyDigest: reuse.DigestString("p"), CostUnits: 1})
	return PhysicalPlan{PlanID: fmt.Sprintf("fan-%d", n),
		Job:      LogicalJob{JobID: "fan", RequiredFinal: "bundle", TerminalVerifier: vc("f.bundle")},
		Nodes:    nodes,
		Terminal: "bundle", Policy: reuse.DigestString("plan-default")}
}

func fanRegistry() *Registry {
	r := standardRegistry()
	for _, op := range []string{"f.root", "f.mid", "f.consumer", "f.bundle"} {
		regOp(r, op)
	}
	return r
}

func countExec(reps ...*TreatResult) map[string]int {
	m := map[string]int{}
	for _, r := range reps {
		for _, id := range r.Executed {
			m[id]++
		}
	}
	return m
}

// TestSharedFanDedup: mid executes once for 3 consumers, all treatments.
func TestSharedFanDedup(t *testing.T) {
	for _, treat := range []string{TreatP0, TreatP1, TreatP2} {
		e := testExecutor(t)
		e.Reg = fanRegistry()
		plan := fanPlan(3, reuse.DigestString("root-1"))
		r := runTreat(t, e, plan, treat, "fan")
		if !r.TerminalOK {
			t.Fatalf("%s terminal rejected", treat)
		}
		if c := countExec(r)["mid"]; c != 1 {
			t.Fatalf("%s: mid executed %d times, want 1", treat, c)
		}
	}
}

// TestSharedInvalidationOneRecompute: root change → mid recomputes exactly once,
// repopulating all three consumers; finals match oracle.
func TestSharedInvalidationOneRecompute(t *testing.T) {
	e := testExecutor(t)
	e.Reg = fanRegistry()
	plan := fanPlan(3, reuse.DigestString("root-1"))
	runTreat(t, e, plan, TreatP2, "warm")
	mut := fanPlan(3, reuse.DigestString("root-2"))
	// Oracle with fan registry (isolated store).
	e2 := testExecutor(t)
	e2.Reg = fanRegistry()
	orep, err := e2.Run(mut, TreatP0, "oracle", 0)
	if err != nil || !orep.TerminalOK {
		t.Fatalf("fan oracle failed: %v", err)
	}
	want := orep.FinalBytes
	r := runTreat(t, e, mut, TreatP2, "mut")
	if !r.TerminalOK || r.Final != want {
		t.Fatalf("fan recompute final %s != %s", r.Final, want)
	}
	if c := countExec(r)["mid"]; c != 1 {
		t.Fatalf("mid recomputed %d times, want exactly 1", c)
	}
	if !equalSet(r.Executed, map[string]bool{"root": true, "mid": true, "c0": true, "c1": true, "c2": true, "bundle": true}) {
		t.Fatalf("fan closure wrong: %v", r.Executed)
	}
}

// TestD1TwoOutputs: attest-plan then summary-plan share normalize; normalize
// executes once total under P2 (and under P1 via the store).
func TestD1TwoOutputs(t *testing.T) {
	for _, treat := range []string{TreatP1, TreatP2} {
		e := testExecutor(t)
		staged, _ := buildD1(55, d1Doc(55))
		attestPlan := staged // terminal attest
		r1 := runTreat(t, e, attestPlan, treat, "out1")
		sumPlan := staged
		sumPlan.Terminal = "summary"
		r2 := runTreat(t, e, sumPlan, treat, "out2")
		if !r1.TerminalOK || !r2.TerminalOK {
			t.Fatalf("%s outputs rejected", treat)
		}
		total := countExec(r1, r2)
		if total["normalize"] != 1 {
			t.Fatalf("%s: normalize executed %d times across 2 outputs, want 1: %v",
				treat, total["normalize"], total)
		}
		if treat == TreatP2 && asSet(r1.Executed)["summary"] {
			t.Fatalf("P2 output1 should skip summary: %v", r1.Executed)
		}
	}
}
