package replan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/plan"
	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// runtime.go: the deterministic runtime replan loop (Phase 4).

// Runtime executes one logical job with mid-run replanning.
type Runtime struct {
	Ex       *plan.Executor
	WS       WorldState
	Alts     func(ws WorldState) []plan.PhysicalPlan
	Effects  map[string]EffectSpec // op -> spec; absent = PURE
	Schedule []ScheduledTrigger
	Budget   int // -1 = unlimited
	StateDir string
	// CrashAfter aborts the run with a simulated crash after this many total
	// node executions (first run only; 0 = disabled). Used to prove
	// crash/restart reconstruction of switches and effect boundaries.
	CrashAfter int
	// Frozen disables switching (R1 treatment): assessments still run and
	// would-be switches are recorded in WouldSwitch, but the active plan
	// never changes.
	Frozen      bool
	WouldSwitch []SwitchRecord

	activeID       string
	activeTerminal string
	spent          int
	applied        map[string]bool
	appliedOrder   []string
	switches       []SwitchRecord
	executed       map[string]execRec // key digest -> record
	effects        map[string]EffectRecord
	failedOps      map[string]bool
	failedVer      map[string]bool
	unknownSc      map[string]bool
	seenPlan       map[string]plan.PhysicalPlan
	replanNS       int64
	assessments    int
}

type execRec struct {
	PlanID string
	NodeID string
	Units  int
}

// NewRuntime builds a runtime. Alts rebuilds candidates from the live world.
// The world state is deep-copied: trigger mutations never leak back to the
// caller (test isolation and honest R1/R2 comparisons on shared fixtures).
func NewRuntime(ex *plan.Executor, ws WorldState, alts func(WorldState) []plan.PhysicalPlan, stateDir string) *Runtime {
	return &Runtime{Ex: ex, WS: copyWorld(ws), Alts: alts, Effects: map[string]EffectSpec{},
		Budget: -1, StateDir: stateDir, applied: map[string]bool{},
		executed: map[string]execRec{}, effects: map[string]EffectRecord{},
		failedOps: map[string]bool{}, failedVer: map[string]bool{},
		unknownSc: map[string]bool{}, seenPlan: map[string]plan.PhysicalPlan{}}
}

func copyWorld(w WorldState) WorldState {
	out := w
	out.CostOverride = map[string]int{}
	for k, v := range w.CostOverride {
		out.CostOverride[k] = v
	}
	out.OpVersion = map[string]string{}
	for k, v := range w.OpVersion {
		out.OpVersion[k] = v
	}
	out.VerifierOf = map[string]string{}
	for k, v := range w.VerifierOf {
		out.VerifierOf[k] = v
	}
	out.Extra = map[string]string{}
	for k, v := range w.Extra {
		out.Extra[k] = v
	}
	return out
}

// RuntimeReport is the auditable job record.
type RuntimeReport struct {
	FinalBytes   string         `json:"final_bytes"`
	TerminalOK   bool           `json:"terminal_accepted"`
	ActivePlan   string         `json:"active_plan"`
	SpentUnits   int            `json:"spent_units"`
	Switches     []SwitchRecord `json:"switches"`
	Assessments  int            `json:"assessments"`
	ReplanUS     int64          `json:"replan_overhead_us"`
	ExecutedKeys int            `json:"executed_keys"`
	WastedUnits  int            `json:"wasted_units"`
	NoLegal      string         `json:"no_legal_suffix,omitempty"`
	// DroppedTriggers lists scheduled trigger IDs never applied because
	// their node was never visited (e.g. abandoned-plan nodes post-switch).
	// Dropped is recorded, never silent.
	DroppedTriggers []string `json:"dropped_triggers"`
}

// effectOf returns the spec for an op (default PURE).
func (r *Runtime) effectOf(op string) EffectSpec {
	if s, ok := r.Effects[op]; ok {
		return s
	}
	return EffectSpec{Class: EffectPure}
}

// applyEvent mutates authoritative state. It is deterministic and idempotent
// per event ID (callers dedup).
func (r *Runtime) applyEvent(ev Event) {
	switch ev.Kind {
	case TrigDepInvalidated:
		switch ev.InputName {
		case "lintcfg":
			r.WS.LintCfg = ev.NewDigest
		case "toolchain":
			r.WS.Toolchain = ev.NewDigest
		case "tree":
			r.WS.Tree = ev.NewDigest
		case "doc":
			r.WS.Doc = ev.NewDigest
		case "op-version":
			if r.WS.OpVersion == nil {
				r.WS.OpVersion = map[string]string{}
			}
			r.WS.OpVersion[ev.Node] = ev.NewDigest
		case "verifier":
			if r.WS.VerifierOf == nil {
				r.WS.VerifierOf = map[string]string{}
			}
			r.WS.VerifierOf[ev.Node] = ev.NewDigest
		default:
			// "extra:<key>" writes scenario inputs into WS.Extra.
			if len(ev.InputName) > 6 && ev.InputName[:6] == "extra:" {
				if r.WS.Extra == nil {
					r.WS.Extra = map[string]string{}
				}
				r.WS.Extra[ev.InputName[6:]] = ev.NewDigest
			}
		}
	case TrigArtifactArrived:
		a := ev.Arrival
		r.Ex.Outcomes[a.JobID] = reuse.Outcome{JobID: a.JobID, Version: 1, Status: "ACCEPTED", Found: true}
		r.Ex.Store.SetOutcome(r.Ex.Outcomes[a.JobID])
		if _, err := r.Ex.Store.Publish(a.Key, reuse.PublishBody{
			ArtifactDigest: reuse.DigestBytes(a.Bytes),
			Verification:   reuse.VerificationAccepted,
			EvidenceID:     a.EvidenceID, VerifiedAt: "2026-09-29T00:00:00Z",
			ActualCostUSD: a.CostUSD, ReceiptID: a.ReceiptID,
			OutcomeJobID: a.JobID, OutcomeVersion: 1,
		}); err != nil {
			panic(fmt.Sprintf("replan: arrival publish failed: %v", err))
		}
		r.Ex.Bodies[reuse.DigestBytes(a.Bytes)] = a.Bytes
		r.appendLog(map[string]string{"t": "outcome", "job": a.JobID,
			"version": "1", "status": "ACCEPTED"})
	case TrigArtifactRevoked:
		cur := r.Ex.Outcomes[ev.JobID]
		cur.Version++
		cur.Status = "REJECTED"
		cur.Found = true
		r.Ex.Outcomes[ev.JobID] = cur
		r.Ex.Store.SetOutcome(cur)
		r.Ex.Store.PropagateCorrection(ev.JobID, cur.Version, "REJECTED")
		r.appendLog(map[string]string{"t": "outcome", "job": ev.JobID,
			"version": fmt.Sprint(cur.Version), "status": "REJECTED"})
	case TrigVerifyFailed:
		c := plan.ContractFor(ev.Op)
		r.failedVer[c] = true
		r.Ex.Reg.Verifiers[c] = func(plan.PlanNode, map[string][]byte, []byte) bool { return false }
	case TrigOpFailed, TrigExecutorDown:
		r.failedOps[ev.Op] = true
		op := ev.Op
		r.Ex.Reg.Ops[op] = func(n plan.PlanNode, in map[string][]byte) ([]byte, error) {
			return nil, fmt.Errorf("replan: op %s unavailable", op)
		}
	case TrigCostChanged:
		if r.WS.CostOverride == nil {
			r.WS.CostOverride = map[string]int{}
		}
		r.WS.CostOverride[ev.Node] = ev.NewCost
	case TrigBudgetReduced:
		r.Budget = ev.Budget
	case TrigEffectUnknown:
		r.unknownSc[ev.Scope] = true
	}
}

// candidateQuotes rebuilds alternatives from the live world, quotes each, and
// applies runtime legality filters (failed ops/verifiers, effect compat,
// budget). Returns legal quotes + rejection reasons.
func (r *Runtime) candidateQuotes() (legal map[string]plan.PlanQuote, rejected map[string]string, byID map[string]plan.PhysicalPlan) {
	legal = map[string]plan.PlanQuote{}
	rejected = map[string]string{}
	byID = map[string]plan.PhysicalPlan{}
	t0 := time.Now()
	defer func() { r.replanNS += time.Since(t0).Nanoseconds() }()
	for _, p := range r.Alts(r.WS) {
		byID[p.PlanID] = p
		r.seenPlan[p.PlanID] = p
		q := r.Ex.Quote(p)
		if !q.Legal {
			rejected[p.PlanID] = "quote: " + q.Reason
			continue
		}
		if reason := r.compatReason(p, q); reason != "" {
			rejected[p.PlanID] = reason
			continue
		}
		legal[p.PlanID] = q
	}
	return legal, rejected, byID
}

// compatReason enforces effect compatibility, failure avoidance and budget on
// nodes the quote says must execute (price > 0).
func (r *Runtime) compatReason(p plan.PhysicalPlan, q plan.PlanQuote) string {
	byID := map[string]plan.PlanNode{}
	for _, n := range p.Nodes {
		byID[n.NodeID] = n
	}
	ids := make([]string, 0, len(q.NodePrices))
	for id := range q.NodePrices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if q.NodePrices[id] == 0 {
			continue // reuse: no new execution, no new effect
		}
		n := byID[id]
		if r.failedOps[n.Op] {
			return fmt.Sprintf("node %q op %q failed", id, n.Op)
		}
		if n.VerifierContract != "" && r.failedVer[n.VerifierContract] {
			return fmt.Sprintf("node %q verifier failed", id)
		}
		spec := r.effectOf(n.Op)
		kd := q.NodeKeys[id]
		if rec, done := r.executed[kd]; done && kd != "" {
			// Re-execution of an already-executed key.
			if spec.Class != EffectPure {
				return fmt.Sprintf("node %q would re-apply %s effect from %s/%s",
					id, spec.Class, rec.PlanID, rec.NodeID)
			}
			// PURE re-execution is safe (republish path) but never needed
			// when the record is VALID; allow (executor decides).
		}
		if spec.Class != EffectPure && kd != "" {
			if prev, ok := r.effects[spec.Scope]; ok && prev.Digest != kd {
				return fmt.Sprintf("node %q %s scope %q already committed differently",
					id, spec.Class, spec.Scope)
			}
			if r.unknownSc[spec.Scope] {
				return fmt.Sprintf("node %q scope %q effect UNKNOWN", id, spec.Scope)
			}
		}
	}
	remaining := q.TotalUnits
	if r.Budget >= 0 && r.spent+remaining > r.Budget {
		return fmt.Sprintf("remaining %d exceeds budget %d (spent %d)", remaining, r.Budget, r.spent)
	}
	return ""
}

// assess selects the cheapest legal remaining plan. Deterministic:
// lowest total, then lexicographic plan ID.
func (r *Runtime) assess(triggerID string, kind TriggerKind) (plan.PhysicalPlan, map[string]plan.PhysicalPlan, map[string]plan.PlanQuote, map[string]string, error) {
	legal, rejected, byID := r.candidateQuotes()
	r.assessments++
	if len(legal) == 0 {
		return plan.PhysicalPlan{}, byID, nil, rejected, &NoLegalSuffix{TriggerID: triggerID,
			Reason: fmt.Sprintf("all %d candidates rejected: %v", len(rejected), rejected), Spent: r.spent}
	}
	type scored struct {
		id string
		q  plan.PlanQuote
	}
	var ss []scored
	for id, q := range legal {
		ss = append(ss, scored{id, q})
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].q.TotalUnits != ss[j].q.TotalUnits {
			return ss[i].q.TotalUnits < ss[j].q.TotalUnits
		}
		return ss[i].id < ss[j].id
	})
	best := ss[0]
	quotes := map[string]plan.PlanQuote{}
	for _, s := range ss {
		quotes[s.id] = s.q
	}
	return byID[best.id], byID, quotes, rejected, nil
}

// accountExec records newly executed nodes (spent + keys + effects).
func (r *Runtime) accountExec(active plan.PhysicalPlan, rep *plan.RunReport) {
	byID := map[string]plan.PlanNode{}
	for _, n := range active.Nodes {
		byID[n.NodeID] = n
	}
	for _, id := range rep.Executed {
		kd := rep.NodeKeys[id]
		if _, ok := r.executed[kd]; ok && kd != "" {
			continue
		}
		n := byID[id]
		units := n.CostUnits + plan.VerifyPriceUnits
		r.spent += units
		if kd != "" {
			r.executed[kd] = execRec{PlanID: active.PlanID, NodeID: id, Units: units}
			r.appendLog(map[string]string{"t": "exec", "key": kd,
				"plan": active.PlanID, "node": id, "units": fmt.Sprint(units)})
		}
		spec := r.effectOf(n.Op)
		if spec.Class != EffectPure && kd != "" {
			if prev, ok := r.effects[spec.Scope]; !ok || prev.Digest == kd {
				r.effects[spec.Scope] = EffectRecord{Scope: spec.Scope, Digest: kd,
					Key: kd, Status: "committed", NodeID: id}
				r.appendLog(map[string]string{"t": "effect", "scope": spec.Scope,
					"digest": kd, "node": id})
			}
		}
	}
}

// Run executes the job to completion with replanning. runID scopes restart.
func (r *Runtime) Run(runID string) (*RuntimeReport, error) {
	// Initial selection on the starting world.
	legal, rejected, byID := r.candidateQuotes()
	r.assessments++
	if len(legal) == 0 {
		return nil, &NoLegalSuffix{TriggerID: "init", Reason: fmt.Sprintf("%v", rejected)}
	}
	best, quotes := pickBest(legal)
	_ = quotes
	r.activeID = best
	r.activeTerminal = byID[best].Terminal
	active := byID[best]
	r.appendLog(map[string]string{"t": "start", "plan": best, "budget": fmt.Sprint(r.Budget)})
	// Drain before-start triggers (AfterNode ""): assess once before any node
	// executes, so pre-effect replans never commit work they then abandon.
	if r.drainStartTriggers() {
		next, _, _, _, aerr := r.assess("start", "replan")
		if aerr != nil {
			var nls *NoLegalSuffix
			if errors.As(aerr, &nls) {
				return r.report(active, nil, nls.Reason), aerr
			}
			return nil, aerr
		}
		if next.PlanID != r.activeID && !r.Frozen {
			r.switches = append(r.switches, SwitchRecord{Seq: 0,
				TriggerID: "start", OldPlan: r.activeID, NewPlan: next.PlanID,
				SpentUnits: 0})
			r.appendLog(map[string]string{"t": "switch", "old": r.activeID, "new": next.PlanID})
			r.activeID = next.PlanID
			r.activeTerminal = next.Terminal
			active = next
		}
	}
	for {
		// Crash budget is cumulative across loop iterations (total executed
		// keys), so crashes land deterministically mid-remaining-work even
		// after aborts and switches.
		callBudget := 0
		if r.CrashAfter > 0 {
			callBudget = r.CrashAfter - len(r.executed)
			if callBudget <= 0 {
				callBudget = 1
			}
		}
		rep, err := r.Ex.RunWithHook(active, plan.TreatP2, runID, callBudget,
			func(afterNode string, rep *plan.RunReport) plan.HookAction {
				r.accountExec(active, rep)
				fired := false
				for i := range r.Schedule {
					st := &r.Schedule[i]
					if st.AfterNode == afterNode && !r.applied[st.Event.ID] {
						r.applied[st.Event.ID] = true
						r.appliedOrder = append(r.appliedOrder, st.Event.ID)
						r.applyEvent(st.Event)
						evJSON, _ := json.Marshal(st.Event)
						r.appendLog(map[string]string{"t": "trigger", "id": st.Event.ID,
							"kind": string(st.Event.Kind), "after": afterNode,
							"event": string(evJSON)})
						fired = true
					}
				}
				if fired {
					return plan.HookAbort
				}
				// Budget trip-wire after every node.
				if r.Budget >= 0 && r.spent > r.Budget {
					ev := Event{ID: "auto-budget@" + afterNode, Kind: TrigBudgetReduced, Budget: r.Budget}
					r.applied[ev.ID] = true
					r.appliedOrder = append(r.appliedOrder, ev.ID)
					r.appendLog(map[string]string{"t": "trigger", "id": ev.ID,
						"kind": string(ev.Kind), "after": afterNode})
					return plan.HookAbort
				}
				return plan.HookContinue
			})
		if err != nil && !errors.Is(err, plan.ErrHookAbort) {
			return nil, err
		}
		if errors.Is(err, plan.ErrHookAbort) {
			next, byID, quotes, rejected, aerr := r.assess(r.lastTrigger(), "replan")
			if aerr != nil {
				var nls *NoLegalSuffix
				if errors.As(aerr, &nls) {
					return r.report(active, rep, nls.Reason), aerr
				}
				return nil, aerr
			}
			if next.PlanID != r.activeID {
				rec := SwitchRecord{Seq: len(r.switches),
					TriggerID: r.lastTrigger(), OldPlan: r.activeID, NewPlan: next.PlanID,
					Quotes: quoteTotals(quotes), Rejected: rejected, SpentUnits: r.spent}
				if r.Frozen {
					r.WouldSwitch = append(r.WouldSwitch, rec)
					// Frozen shape, live bindings: continue the same plan ID
					// rebuilt from the current world (honest static-plan
					// semantics — keys and costs track reality, structure
					// never changes).
					if fresh, ok := byID[r.activeID]; ok {
						active = fresh
						r.activeTerminal = fresh.Terminal
					}
				} else {
					r.switches = append(r.switches, rec)
					r.appendLog(map[string]string{"t": "switch", "old": r.activeID, "new": next.PlanID})
					r.activeID = next.PlanID
					r.activeTerminal = next.Terminal
				}
			}
			// Fresh-object adoption (always): the world may have moved, and a
			// stale object would re-execute nodes with stale input bindings
			// (old-state artifacts satisfying new-state nodes — the hard
			// gate). Non-frozen adopts the winner (possibly a new shape);
			// frozen adopts the same-ID rebuild (frozen shape, live
			// bindings), so frozen executions stay correct as well as static.
			if r.Frozen {
				if fresh, ok := byID[r.activeID]; ok {
					active = fresh
					r.activeTerminal = fresh.Terminal
				}
			} else {
				active = next
				r.activeTerminal = next.Terminal
			}
			continue // resume same runID: completed work carries over
		}
		// Completed without abort: apply nothing further; terminal decided inside.
		return r.report(active, rep, ""), nil
	}
}

func (r *Runtime) lastTrigger() string {
	if len(r.appliedOrder) == 0 {
		return "budget-trip"
	}
	return r.appliedOrder[len(r.appliedOrder)-1]
}

// report builds the final auditable record, computing wasted units as
// executed work outside the final artifact's key closure.
func (r *Runtime) report(active plan.PhysicalPlan, rep *plan.RunReport, noLegal string) *RuntimeReport {
	out := &RuntimeReport{ActivePlan: active.PlanID, SpentUnits: r.spent,
		Switches: r.switches, Assessments: r.assessments,
		ReplanUS: r.replanNS / 1000, ExecutedKeys: len(r.executed), NoLegal: noLegal}
	for _, st := range r.Schedule {
		if !r.applied[st.Event.ID] {
			out.DroppedTriggers = append(out.DroppedTriggers, st.Event.ID)
		}
	}
	if rep != nil {
		out.FinalBytes = rep.FinalBytes
		out.TerminalOK = rep.TerminalOK
		out.WastedUnits = r.wasted(rep)
	}
	return out
}

// wasted walks the final artifact's upstream-key closure; executed keys
// outside it are abandoned work.
func (r *Runtime) wasted(rep *plan.RunReport) int {
	closure := map[string]bool{}
	queue := []string{}
	if kd, ok := rep.NodeKeys[r.activeTerminal]; ok && kd != "" {
		queue = append(queue, kd)
	}
	for len(queue) > 0 {
		kd := queue[0]
		queue = queue[1:]
		if closure[kd] {
			continue
		}
		closure[kd] = true
		if art, ok := r.Ex.Store.Lookup(kd); ok {
			for _, d := range art.Deps {
				if len(d.Name) > len(reuse.UpstreamPrefix) &&
					d.Name[:len(reuse.UpstreamPrefix)] == reuse.UpstreamPrefix {
					uk := d.Name[len(reuse.UpstreamPrefix):]
					if !closure[uk] {
						queue = append(queue, uk)
					}
				}
			}
		}
	}
	w := 0
	for kd, rec := range r.executed {
		if !closure[kd] {
			w += rec.Units
		}
	}
	return w
}

func pickBest(legal map[string]plan.PlanQuote) (string, map[string]plan.PlanQuote) {
	type scored struct {
		id string
		q  plan.PlanQuote
	}
	var ss []scored
	for id, q := range legal {
		ss = append(ss, scored{id, q})
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].q.TotalUnits != ss[j].q.TotalUnits {
			return ss[i].q.TotalUnits < ss[j].q.TotalUnits
		}
		return ss[i].id < ss[j].id
	})
	return ss[0].id, legal
}

func quoteTotals(q map[string]plan.PlanQuote) map[string]int {
	out := map[string]int{}
	for id, qq := range q {
		out[id] = qq.TotalUnits
	}
	return out
}

func (r *Runtime) appendLog(fields map[string]string) {
	if r.StateDir == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(r.StateDir, "runtime.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(fields)
	line = append(line, '\n')
	_, _ = f.Write(line)
	_ = f.Sync()
}

// drainStartTriggers applies all unapplied AfterNode=="" triggers. True if any fired.
func (r *Runtime) drainStartTriggers() bool {
	fired := false
	for i := range r.Schedule {
		st := &r.Schedule[i]
		if st.AfterNode == "" && !r.applied[st.Event.ID] {
			r.applied[st.Event.ID] = true
			r.appliedOrder = append(r.appliedOrder, st.Event.ID)
			r.applyEvent(st.Event)
			evJSON, _ := json.Marshal(st.Event)
			r.appendLog(map[string]string{"t": "trigger", "id": st.Event.ID,
				"kind": string(st.Event.Kind), "after": "", "event": string(evJSON)})
			fired = true
		}
	}
	return fired
}

// Resume rebuilds a runtime from the durable log (crash/restart): applied
// triggers are re-applied to a fresh world (replay, in logged order),
// effects/spent/switches restored. The caller passes the same schedule (for
// trigger identity) and runID (progress file shared with the crashed run).
// Deterministic: identical history replays identically.
func Resume(ex *plan.Executor, ws WorldState, alts func(WorldState) []plan.PhysicalPlan,
	schedule []ScheduledTrigger, stateDir string) *Runtime {
	rt := NewRuntime(ex, ws, alts, "")
	rt.Schedule = schedule
	raw, err := os.ReadFile(filepath.Join(stateDir, "runtime.log"))
	if err != nil {
		return rt
	}
	byID := map[string]Event{}
	for _, st := range schedule {
		byID[st.Event.ID] = st.Event
	}
	for _, line := range splitLogLines(raw) {
		var f map[string]string
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			continue
		}
		switch f["t"] {
		case "trigger":
			var ev Event
			if ej, ok := f["event"]; ok && ej != "" {
				_ = json.Unmarshal([]byte(ej), &ev)
			} else if se, ok := byID[f["id"]]; ok {
				ev = se
			} else {
				continue
			}
			// Replay onto the fresh world WITHOUT logging (log already has it).
			// Revocation replays exactly: restore the logged outcome version
			// rather than bumping (idempotent replay, not re-application).
			if ev.Kind == TrigArtifactRevoked {
				continue // handled by the paired "outcome" line below
			}
			saved := rt.StateDir
			rt.StateDir = ""
			rt.applyEventWithID(ev)
			rt.StateDir = saved
			rt.applied[ev.ID] = true
			rt.appliedOrder = append(rt.appliedOrder, ev.ID)
		case "outcome":
			var ver uint64
			_, _ = fmt.Sscanf(f["version"], "%d", &ver)
			o := reuse.Outcome{JobID: f["job"], Version: ver, Status: f["status"], Found: true}
			rt.Ex.Outcomes[f["job"]] = o
			rt.Ex.Store.SetOutcome(o)
			if f["status"] == "REJECTED" && ver > 1 {
				rt.Ex.Store.PropagateCorrection(f["job"], ver, "REJECTED")
			}
			// Mark the paired revocation trigger applied (it is not re-fired).
			for _, st := range schedule {
				if st.Event.Kind == TrigArtifactRevoked && st.Event.JobID == f["job"] {
					rt.applied[st.Event.ID] = true
					rt.appliedOrder = append(rt.appliedOrder, st.Event.ID)
				}
			}
		case "exec":
			var units int
			_, _ = fmt.Sscanf(f["units"], "%d", &units)
			rt.executed[f["key"]] = execRec{PlanID: f["plan"], NodeID: f["node"], Units: units}
			rt.spent += units
		case "effect":
			rt.effects[f["scope"]] = EffectRecord{Scope: f["scope"], Digest: f["digest"],
				Key: f["digest"], Status: "committed", NodeID: f["node"]}
		case "switch":
			rt.switches = append(rt.switches, SwitchRecord{Seq: len(rt.switches),
				OldPlan: f["old"], NewPlan: f["new"], SpentUnits: rt.spent})
			rt.activeID = f["new"]
		case "start":
			if rt.activeID == "" {
				rt.activeID = f["plan"]
			}
			if b, ok := f["budget"]; ok && b != "" {
				var bi int
				if _, err := fmt.Sscanf(b, "%d", &bi); err == nil {
					rt.Budget = bi
				}
			}
		}
	}
	rt.StateDir = stateDir
	return rt
}

// applyEventWithID applies an event (replay path shares applyEvent).
func (r *Runtime) applyEventWithID(ev Event) { r.applyEvent(ev) }

func splitLogLines(raw []byte) []string {
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
