package plan

import (
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

func testRegistry() *Registry {
	r := standardRegistry()
	for _, op := range []string{"d1.validate", "d1.extract", "d1.normalize", "d1.aggregate",
		"d1.report", "d1.attest", "d1.summary", "d1.direct",
		"d2.identity", "d2.compile", "d2.setup", "d2.unit", "d2.lint",
		"d2.aggregate", "d2.attest", "d2.direct"} {
		regOp(r, op)
	}
	// Predeclared rotated contract for the verifier-change scenario.
	r.Verifiers[reuse.DigestString("contract:d1.summary/v2")] = digestVerify
	return r
}

func testExecutor(t *testing.T) *Executor {
	t.Helper()
	s, err := reuse.Open(filepath.Join(t.TempDir(), "plan.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &Executor{Store: s, Reg: testRegistry(), Outcomes: map[string]reuse.Outcome{},
		Bodies: map[string][]byte{}, StateDir: t.TempDir()}
}

func TestPlanCanonicalDeterminism(t *testing.T) {
	a, _ := buildD1(11, d1Doc(11))
	b, _ := buildD1(11, d1Doc(11))
	// Shuffle node order: digest must be identical.
	for i, j := 0, len(b.Nodes)-1; i < j; i, j = i+1, j-1 {
		b.Nodes[i], b.Nodes[j] = b.Nodes[j], b.Nodes[i]
	}
	if string(a.Canonical()) != string(b.Canonical()) {
		t.Fatal("plan canonical form depends on node order")
	}
	if a.Digest() != b.Digest() {
		t.Fatal("plan digest depends on node order")
	}
	c, _ := buildD1(12, d1Doc(12))
	if c.Digest() == a.Digest() {
		t.Fatal("different inputs, same plan digest")
	}
}

func TestTopoOrderRespectsEdges(t *testing.T) {
	staged, _ := buildD1(11, d1Doc(11))
	order, err := staged.TopoOrder()
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	byID := map[string]PlanNode{}
	for _, n := range staged.Nodes {
		byID[n.NodeID] = n
	}
	for _, n := range staged.Nodes {
		for _, u := range n.Upstreams {
			if pos[u] >= pos[n.NodeID] {
				t.Fatalf("edge %s->%s violated in order %v", u, n.NodeID, order)
			}
		}
	}
}

func TestCycleRefused(t *testing.T) {
	staged, _ := buildD1(11, d1Doc(11))
	// Introduce a cycle: validate now depends on attest.
	for i, n := range staged.Nodes {
		if n.NodeID == "validate" {
			staged.Nodes[i].Upstreams = []string{"attest"}
		}
	}
	if err := staged.Validate(); err == nil {
		t.Fatal("cyclic plan validated")
	}
	if _, err := staged.TopoOrder(); err == nil {
		t.Fatal("cyclic plan ordered")
	}
	e := testExecutor(t)
	if _, err := e.Run(staged, TreatP2, "cycle", 0); err == nil {
		t.Fatal("cyclic plan executed")
	}
}

func TestDuplicateNodeIDRefused(t *testing.T) {
	staged, _ := buildD1(11, d1Doc(11))
	dup := staged.Nodes[0]
	dup.Op = "d1.other"
	staged.Nodes = append(staged.Nodes, dup)
	if err := staged.Validate(); err == nil {
		t.Fatal("duplicate node id validated")
	}
	// Same id, same definition is still refused (explicit identity, no aliasing).
	staged2, _ := buildD1(11, d1Doc(11))
	staged2.Nodes = append(staged2.Nodes, staged2.Nodes[0])
	if err := staged2.Validate(); err == nil {
		t.Fatal("exact-duplicate node id validated")
	}
}

func TestUnknownUpstreamAndTerminalRefused(t *testing.T) {
	staged, _ := buildD1(11, d1Doc(11))
	staged.Nodes[1].Upstreams = []string{"nope"}
	if err := staged.Validate(); err == nil {
		t.Fatal("unknown upstream validated")
	}
	staged2, _ := buildD1(11, d1Doc(11))
	staged2.Terminal = "nope"
	if err := staged2.Validate(); err == nil {
		t.Fatal("unknown terminal validated")
	}
}

func TestNodeKeyCrossPlanStability(t *testing.T) {
	// Same computation in staged vs a reordered copy: identical keys.
	a, _ := buildD1(11, d1Doc(11))
	b, _ := buildD1(11, d1Doc(11))
	b.PlanID = "other-plan"
	ka := map[string]string{}
	kb := map[string]string{}
	aa := map[string]string{}
	// Resolve bottom-up manually for the shared prefix.
	for _, id := range []string{"validate", "extract", "normalize"} {
		var na, nb PlanNode
		for _, n := range a.Nodes {
			if n.NodeID == id {
				na = n
			}
		}
		for _, n := range b.Nodes {
			if n.NodeID == id {
				nb = n
			}
		}
		xa, err := NodeKey(na, ka, aa)
		if err != nil {
			t.Fatal(err)
		}
		xb, err := NodeKey(nb, kb, aa)
		if err != nil {
			t.Fatal(err)
		}
		if xa.KeyDigest() != xb.KeyDigest() {
			t.Fatalf("node %s key differs across plans", id)
		}
		ka[id], kb[id] = xa.KeyDigest(), xb.KeyDigest()
		aa[id] = reuse.DigestString("shared-bytes-" + id)
	}
}
