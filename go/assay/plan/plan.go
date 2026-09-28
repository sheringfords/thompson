// Package plan implements the execution-plan assay prototype
// (THOMPSON_EXECUTION_PLAN_ASSAY_V1, Phase 2): a generic, deterministic DAG
// executor composed over the UNCHANGED verified-artifact contract in
// go/assay/reuse. Research harness only: bounded DAGs, no dynamic plans, no
// distributed scheduling, no production integration.
package plan

import (
	"fmt"
	"sort"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// NodeResolution is the per-node scheduling decision.
type NodeResolution string

const (
	ResolveReuseValid     NodeResolution = "REUSE_VALID"
	ResolveExecute        NodeResolution = "EXECUTE"
	ResolveVerify         NodeResolution = "VERIFY"
	ResolveBlockedUnknown NodeResolution = "BLOCKED_UNKNOWN"
	ResolveFailed         NodeResolution = "FAILED"
)

// LogicalJob names the desired final result (the what).
type LogicalJob struct {
	JobID            string
	Inputs           []reuse.Dep // canonical input digests
	RequiredFinal    string      // terminal node id
	TerminalVerifier string      // verifier contract digest (hex)
}

// PlanNode is one exact operation producing one artifact.
type PlanNode struct {
	NodeID           string
	Op               string
	OpVersion        string
	Inputs           []reuse.Dep // declared non-upstream deps
	Upstreams        []string    // node ids (edges; explicit only)
	Executor         string
	EnvDigest        string
	VerifierContract string // hex digest; "" = no independent verification
	PolicyDigest     string
	CostUnits        int // synthetic CPU-work units (economics only)
}

// PhysicalPlan is one bounded DAG realizing a logical job.
type PhysicalPlan struct {
	PlanID   string
	Job      LogicalJob
	Nodes    []PlanNode
	Terminal string
	Policy   string // plan-policy digest material
}

// Validate refuses invalid plans BEFORE execution: unknown terminal, unknown
// upstreams, duplicate node ids (conflicting or not), cycles, empty op identity.
func (p PhysicalPlan) Validate() error {
	if p.PlanID == "" {
		return fmt.Errorf("plan: plan id required")
	}
	byID := map[string]PlanNode{}
	for _, n := range p.Nodes {
		if n.NodeID == "" || n.Op == "" || n.OpVersion == "" {
			return fmt.Errorf("plan: node with empty identity")
		}
		if prev, ok := byID[n.NodeID]; ok {
			return fmt.Errorf("plan: duplicate node id %q (prev op %s, new op %s)",
				n.NodeID, prev.Op, n.Op)
		}
		byID[n.NodeID] = n
		for _, d := range n.Inputs {
			if d.Name == "" {
				return fmt.Errorf("plan: node %q has unnamed input", n.NodeID)
			}
		}
	}
	if _, ok := byID[p.Terminal]; !ok {
		return fmt.Errorf("plan: unknown terminal %q", p.Terminal)
	}
	for _, n := range p.Nodes {
		for _, u := range n.Upstreams {
			if _, ok := byID[u]; !ok {
				return fmt.Errorf("plan: node %q has unknown upstream %q", n.NodeID, u)
			}
			if u == n.NodeID {
				return fmt.Errorf("plan: node %q depends on itself", n.NodeID)
			}
		}
	}
	if err := checkCycle(p); err != nil {
		return err
	}
	return nil
}

func checkCycle(p PhysicalPlan) error {
	// Iterative DFS with colors; deterministic (sorted adjacency).
	adj := map[string][]string{}
	for _, n := range p.Nodes {
		up := append([]string(nil), n.Upstreams...)
		sort.Strings(up)
		adj[n.NodeID] = up
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		color[id] = gray
		for _, u := range adj[id] {
			switch color[u] {
			case gray:
				return fmt.Errorf("plan: cycle involving %q and %q", id, u)
			case white:
				if err := visit(u); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}
	ids := []string{}
	for _, n := range p.Nodes {
		ids = append(ids, n.NodeID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == white {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// TopoOrder returns the deterministic topological execution order (Kahn's
// algorithm, lexicographic tie-break): upstreams before dependents.
func (p PhysicalPlan) TopoOrder() ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	indeg := map[string]int{}
	down := map[string][]string{}
	for _, n := range p.Nodes {
		if _, ok := indeg[n.NodeID]; !ok {
			indeg[n.NodeID] = 0
		}
		for _, u := range n.Upstreams {
			indeg[n.NodeID]++
			down[u] = append(down[u], n.NodeID)
		}
	}
	var ready []string
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	var order []string
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		next := append([]string(nil), down[id]...)
		sort.Strings(next)
		for _, v := range next {
			indeg[v]--
			if indeg[v] == 0 {
				ready = append(ready, v)
			}
		}
		sort.Strings(ready)
	}
	if len(order) != len(p.Nodes) {
		return nil, fmt.Errorf("plan: cycle detected during ordering")
	}
	return order, nil
}

// Canonical serializes the plan deterministically (sorted nodes, sorted edges).
func (p PhysicalPlan) Canonical() []byte {
	var sb []byte
	w := func(s string) { sb = append(sb, fmt.Sprintf("%d:%s|", len(s), s)...) }
	w("plan:" + p.PlanID)
	w("term:" + p.Terminal)
	w("job:" + p.Job.JobID)
	w("tver:" + p.Job.TerminalVerifier)
	ins := append([]reuse.Dep(nil), p.Job.Inputs...)
	sort.Slice(ins, func(i, j int) bool { return ins[i].Name < ins[j].Name })
	for _, d := range ins {
		w("jin:" + d.Name + "=" + d.Digest)
	}
	nodes := append([]PlanNode(nil), p.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	for _, n := range nodes {
		w("node:" + n.NodeID)
		w("op:" + n.Op + "@" + n.OpVersion)
		w("exec:" + n.Executor)
		w("env:" + n.EnvDigest)
		w("vc:" + n.VerifierContract)
		w("pol:" + n.PolicyDigest)
		up := append([]string(nil), n.Upstreams...)
		sort.Strings(up)
		for _, u := range up {
			w("up:" + u)
		}
		dp := append([]reuse.Dep(nil), n.Inputs...)
		sort.Slice(dp, func(i, j int) bool { return dp[i].Name < dp[j].Name })
		for _, d := range dp {
			w("in:" + d.Name + "=" + d.Digest)
		}
	}
	w("ppol:" + p.Policy)
	return sb
}

// Digest is the deterministic plan identity.
func (p PhysicalPlan) Digest() string { return reuse.DigestBytes(p.Canonical()) }

// NodeKey derives the node's ExecutionKey (contract: no second authority).
// upstreamKeys maps upstream node id -> upstream KEY digest; upstreamArts maps
// upstream node id -> upstream ARTIFACT digest. Edge dep names use the upstream
// KEY digest (cross-plan stable), carrying the upstream ARTIFACT digest
// (content-bound invalidation).
func NodeKey(n PlanNode, upstreamKeys, upstreamArts map[string]string) (reuse.ExecutionKey, error) {
	deps := append([]reuse.Dep(nil), n.Inputs...)
	upIDs := make([]string, 0, len(upstreamKeys))
	for id := range upstreamKeys {
		upIDs = append(upIDs, id)
	}
	sort.Strings(upIDs)
	for _, id := range upIDs {
		art, ok := upstreamArts[id]
		if !ok || art == "" {
			return reuse.ExecutionKey{}, fmt.Errorf("plan: upstream %q has no artifact (fail-closed)", id)
		}
		deps = append(deps, reuse.Dep{Name: reuse.UpstreamPrefix + upstreamKeys[id], Digest: art})
	}
	// Node's own input digest binds its declared non-upstream inputs.
	var ib []byte
	srt := append([]reuse.Dep(nil), n.Inputs...)
	sort.Slice(srt, func(i, j int) bool { return srt[i].Name < srt[j].Name })
	for _, d := range srt {
		ib = append(ib, fmt.Sprintf("%d:%s|%s|", len(d.Name), d.Name, d.Digest)...)
	}
	k := reuse.ExecutionKey{
		Operation:        n.Op,
		OperationVersion: n.OpVersion,
		InputDigest:      reuse.DigestBytes(ib),
		Deps:             deps,
		Executor:         n.Executor,
		EnvDigest:        n.EnvDigest,
		VerifierContract: n.VerifierContract,
		PolicyDigest:     n.PolicyDigest,
	}
	if k.VerifierContract == "" {
		// Nodes without an independent verifier bind a fixed empty-contract
		// marker: they are never independently reusable as verified results,
		// only as content-bound intermediates. The terminal node MUST carry
		// a real contract (enforced by ValidateTerminal/Execute).
		k.VerifierContract = reuse.DigestString("no-independent-verifier")
	}
	if err := k.Validate(); err != nil {
		return reuse.ExecutionKey{}, err
	}
	return k, nil
}
