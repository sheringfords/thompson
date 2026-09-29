package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// OpFunc is one deterministic operation: inputs (upstream bytes by node id)
// plus node params produce output bytes. Implementations do real synthetic
// CPU work scaled by CostUnits (reported explicitly as synthetic systems work).
type OpFunc func(node PlanNode, inputs map[string][]byte) ([]byte, error)

// VerifierFunc independently checks node output bytes.
type VerifierFunc func(node PlanNode, inputs map[string][]byte, output []byte) bool

// Registry maps op identity -> implementation + verifier.
type Registry struct {
	Ops       map[string]OpFunc
	Verifiers map[string]VerifierFunc // keyed by verifier-contract LABEL; see Workloads
	Contracts map[string]string       // op -> contract digest bound at plan build
}

// Executor runs physical plans against a verified-artifact store.
type Executor struct {
	Store    *reuse.Store
	Reg      *Registry
	Outcomes map[string]reuse.Outcome // authoritative outcome view (assay)
	Bodies   map[string][]byte        // assay content store (bytes by digest)
	StateDir string
	jobSeq   int
}

// NodeOutcome tracks one completed node for restart (durable progress).
type NodeOutcome struct {
	NodeID         string `json:"node_id"`
	KeyDigest      string `json:"key_digest"`
	ArtifactDigest string `json:"artifact_digest"`
	OutcomeJobID   string `json:"outcome_job_id"`
	OutcomeVersion uint64 `json:"outcome_version"`
	Verified       bool   `json:"verified"`
}

// RunReport is the auditable per-run record.
type RunReport struct {
	PlanID         string                    `json:"plan_id"`
	Treatment      string                    `json:"treatment"`
	Resolutions    map[string]NodeResolution `json:"resolutions"`
	Reasons        map[string]string         `json:"reasons"`
	Executed       []string                  `json:"executed"`
	Reused         []string                  `json:"reused"`
	Skipped        []string                  `json:"skipped_outside_closure"`
	FinalBytes     string                    `json:"final_artifact_digest"`
	TerminalOK     bool                      `json:"terminal_accepted"`
	PlanOverheadNS int64                     `json:"plan_overhead_ns"`
	OpWorkNS       int64                     `json:"op_work_ns"`
	StoreWriteB    int64                     `json:"store_write_bytes"`
	ExecutionsOf   map[string]int            `json:"executions_of_key"`
	// NodeKeys maps resolved node id -> derived execution-key digest, live
	// through the run (set for every node whose key was derived, including
	// reused and blocked nodes). Downstream assay tooling uses it for
	// cross-run execution accounting.
	NodeKeys map[string]string `json:"node_keys"`
}

// Treatments.
const (
	TreatP0 = "P0" // full recomputation
	TreatP1 = "P1" // independent verified reuse (fixed DAG, per-node, no memo)
	TreatP2 = "P2" // dependency-aware DAG execution (closure + shared memo)
	TreatP3 = "P3" // deterministic physical-plan optimizer over alternatives
)

func (e *Executor) nextJob(planID, node string) string {
	e.jobSeq++
	return fmt.Sprintf("%s/%s/j%d", planID, node, e.jobSeq)
}

func (e *Executor) lookup(jobID string) reuse.Outcome {
	if o, ok := e.Outcomes[jobID]; ok {
		return o
	}
	return reuse.Outcome{}
}

// selectKeys restricts accumulated resolution maps to a node's declared
// upstreams. Without this, unrelated parallel branches leak into keys.
func selectKeys(m map[string]string, upstreams []string) map[string]string {
	out := make(map[string]string, len(upstreams))
	for _, u := range upstreams {
		if v, ok := m[u]; ok {
			out[u] = v
		}
	}
	return out
}

// planClosure returns the terminal's ancestor closure (terminal included).
func planClosure(p PhysicalPlan) map[string]bool {
	byID := map[string]PlanNode{}
	for _, n := range p.Nodes {
		byID[n.NodeID] = n
	}
	out := map[string]bool{}
	var visit func(id string)
	visit = func(id string) {
		if out[id] {
			return
		}
		out[id] = true
		for _, u := range byID[id].Upstreams {
			visit(u)
		}
	}
	visit(p.Terminal)
	return out
}

// statePath is the restart-safe progress file for (plan, run).
func (e *Executor) statePath(runID string) string {
	return filepath.Join(e.StateDir, "run-"+runID+".jsonl")
}

func (e *Executor) loadProgress(runID string) map[string]NodeOutcome {
	out := map[string]NodeOutcome{}
	raw, err := os.ReadFile(e.statePath(runID))
	if err != nil {
		return out
	}
	for _, line := range splitLines(raw) {
		var no NodeOutcome
		if err := json.Unmarshal([]byte(line), &no); err == nil && no.Verified {
			out[no.NodeID] = no
		}
	}
	return out
}

func (e *Executor) recordProgress(runID string, no NodeOutcome) {
	f, err := os.OpenFile(e.statePath(runID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	line, _ := json.Marshal(no)
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		panic(err)
	}
	_ = f.Sync()
}

func splitLines(raw []byte) []string {
	var out []string
	cur := ""
	for _, b := range raw {
		if b == '\n' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		} else {
			cur += string(b)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// HookAction controls RunWithHook after each resolved node.
type HookAction int

const (
	// HookContinue proceeds to the next node.
	HookContinue HookAction = iota
	// HookAbort stops the run after the current node (durable progress kept).
	// RunWithHook returns the partial report with ErrHookAbort.
	HookAbort
)

// NodeHook observes each resolved node. Returning HookAbort stops the run.
type NodeHook func(afterNode string, rep *RunReport) HookAction

// ErrHookAbort is returned by RunWithHook when a hook aborts the run.
var ErrHookAbort = errHookAbort{}

type errHookAbort struct{}

func (errHookAbort) Error() string { return "plan: hook aborted run" }

// Run executes plan under treatment (no hooks).
func (e *Executor) Run(plan PhysicalPlan, treatment, runID string, crashAfter int) (*RunReport, error) {
	return e.RunWithHook(plan, treatment, runID, crashAfter, nil)
}

// RunWithHook executes plan under treatment, invoking hook after each node
// reaches a resolution (including skips and blocks). HookAbort stops the run
// after the current node, keeping durable progress; the partial report is
// returned with ErrHookAbort and finalize is deferred to the resuming run.
func (e *Executor) RunWithHook(plan PhysicalPlan, treatment, runID string, crashAfter int, hook NodeHook) (*RunReport, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	order, err := plan.TopoOrder()
	if err != nil {
		return nil, err
	}
	byID := map[string]PlanNode{}
	for _, n := range plan.Nodes {
		byID[n.NodeID] = n
	}
	if byID[plan.Terminal].VerifierContract == "" {
		return nil, fmt.Errorf("plan: terminal node %q requires an independent verifier contract", plan.Terminal)
	}
	rep := &RunReport{PlanID: plan.PlanID, Treatment: treatment,
		Resolutions: map[string]NodeResolution{}, Reasons: map[string]string{},
		ExecutionsOf: map[string]int{}}
	t0 := time.Now()
	progress := e.loadProgress(runID)
	// P2/P3 closure: only ancestors of the terminal (plus terminal) are
	// required. P1/P0 visit every node in the fixed DAG. Nodes outside the
	// closure are SKIPPED (unscheduled), never executed or reused.
	closure := map[string]bool{}
	if treatment == TreatP2 {
		closure = planClosure(plan)
	}
	upKeys := map[string]string{}  // nodeID -> key digest
	upArts := map[string]string{}  // nodeID -> artifact digest
	upBytes := map[string][]byte{} // nodeID -> content bytes (in-run only)
	blocked := map[string]bool{}
	rep.NodeKeys = upKeys // live view for hooks and post-run accounting
	// fireHook runs the hook (if any); true means abort the run now.
	fireHook := func(id string) (*RunReport, error) {
		if hook != nil && hook(id, rep) == HookAbort {
			rep.PlanOverheadNS = time.Since(t0).Nanoseconds() - rep.OpWorkNS
			return rep, ErrHookAbort
		}
		return nil, nil
	}

	for _, id := range order {
		n := byID[id]
		if treatment == TreatP2 && !closure[id] {
			rep.Skipped = append(rep.Skipped, id)
			rep.Reasons[id] = "outside required terminal closure"
			if r, err := fireHook(id); err != nil {
				return r, err
			}
			continue
		}
		// Upstream failure/block propagates: never consume bad inputs.
		blockedUp := ""
		for _, u := range n.Upstreams {
			if blocked[u] {
				blockedUp = u
				break
			}
			if _, ok := upArts[u]; !ok {
				blockedUp = u
				break
			}
		}
		if blockedUp != "" {
			rep.Resolutions[id] = ResolveBlockedUnknown
			rep.Reasons[id] = "upstream " + blockedUp + " not valid"
			blocked[id] = true
			if r, err := fireHook(id); err != nil {
				return r, err
			}
			continue
		}
		key, err := NodeKey(n, selectKeys(upKeys, n.Upstreams), selectKeys(upArts, n.Upstreams))
		if err != nil {
			rep.Resolutions[id] = ResolveBlockedUnknown
			rep.Reasons[id] = err.Error()
			blocked[id] = true
			if r, err := fireHook(id); err != nil {
				return r, err
			}
			continue
		}
		upKeys[id] = key.KeyDigest()

		// Restart: previously verified node is reused without re-execution.
		if prev, ok := progress[id]; ok && prev.KeyDigest == key.KeyDigest() {
			if art, ok := e.Store.Lookup(key.KeyDigest()); ok && art.State == reuse.ValidityValid {
				dec := e.Store.Evaluate(key, reuse.LiveOf(key), e.lookup)
				if dec.Validity == reuse.ValidityValid {
					upArts[id] = art.ArtifactDigest
					if b, ok := e.Bodies[art.ArtifactDigest]; ok {
						upBytes[id] = b
					}
					rep.Resolutions[id] = ResolveReuseValid
					rep.Reasons[id] = "restart: prior verified completion"
					rep.Reused = append(rep.Reused, id)
					if r, err := fireHook(id); err != nil {
						return r, err
					}
					continue
				}
			}
		}

		switch treatment {
		case TreatP0:
			e.executeNode(plan, n, key, upBytes, runID, treatment, rep, upArts, upBytes)
		case TreatP1:
			e.resolveP1(plan, n, key, upBytes, runID, rep, upArts, upBytes)
		case TreatP2:
			e.resolveP2(plan, n, key, upBytes, runID, rep, upArts, upBytes)
		}
		if r, err := fireHook(id); err != nil {
			return r, err
		}
		if crashAfter > 0 && len(rep.Executed) >= crashAfter {
			return rep, fmt.Errorf("plan: injected crash after %d executions", len(rep.Executed))
		}
	}
	rep.PlanOverheadNS = time.Since(t0).Nanoseconds() - rep.OpWorkNS
	e.finalize(plan, byID, rep, upBytes, upArts)
	return rep, nil
}
