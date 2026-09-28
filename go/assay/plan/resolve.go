package plan

import (
	"errors"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// resolve.go: per-treatment node resolution + finalization.

// executeNode runs the op, verifies, publishes, and records progress.
// Shared-key memo: rep.ExecutionsOf ensures one ExecutionKey executes at most
// once per run — callers check it before invoking (P2) or deliberately skip
// the check (P1 models independent lookups without plan-level dedup).
func (e *Executor) executeNode(plan PhysicalPlan, n PlanNode, key reuse.ExecutionKey,
	upBytes map[string][]byte, runID, treatment string, rep *RunReport,
	upArts map[string]string, sink map[string][]byte) {
	kd := key.KeyDigest()
	op, ok := e.Reg.Ops[n.Op]
	if !ok {
		rep.Resolutions[n.NodeID] = ResolveFailed
		rep.Reasons[n.NodeID] = "unknown op " + n.Op
		return
	}
	// Scope inputs to declared upstreams only (deterministic, no leakage).
	in := map[string][]byte{}
	for _, u := range n.Upstreams {
		if b, ok := upBytes[u]; ok {
			in[u] = b
		}
	}
	t0 := time.Now()
	out, err := op(n, in)
	rep.OpWorkNS += time.Since(t0).Nanoseconds()
	if err != nil {
		rep.Resolutions[n.NodeID] = ResolveFailed
		rep.Reasons[n.NodeID] = "op failed: " + err.Error()
		return
	}
	vf, ok := e.Reg.Verifiers[n.VerifierContract]
	if !ok {
		rep.Resolutions[n.NodeID] = ResolveFailed
		rep.Reasons[n.NodeID] = "no verifier for contract (fail-closed)"
		return
	}
	if !vf(n, in, out) {
		rep.Resolutions[n.NodeID] = ResolveFailed
		rep.Reasons[n.NodeID] = "independent verification FAILED"
		return
	}
	job := e.nextJob(plan.PlanID, n.NodeID)
	// P0 is the no-reuse oracle: it verifies but never publishes, so repeated
	// ground-truth runs cannot conflict with (or pollute) the shared store.
	if treatment == TreatP0 {
		e.Bodies[reuse.DigestBytes(out)] = out
		upArts[n.NodeID] = reuse.DigestBytes(out)
		sink[n.NodeID] = out
		rep.ExecutionsOf[kd]++
		rep.Executed = append(rep.Executed, n.NodeID)
		rep.Resolutions[n.NodeID] = ResolveVerify
		rep.Reasons[n.NodeID] = "P0 oracle execution (verified, not published)"
		return
	}
	e.Outcomes[job] = reuse.Outcome{JobID: job, Version: 1, Status: "ACCEPTED", Found: true}
	e.Store.SetOutcome(e.Outcomes[job])
	art, err := e.Store.Publish(key, reuse.PublishBody{
		ArtifactDigest: reuse.DigestBytes(out),
		Verification:   reuse.VerificationAccepted,
		EvidenceID:     "ev-" + job,
		VerifiedAt:     "2026-09-29T00:00:00Z",
		ActualCostUSD:  float64(n.CostUnits) * 0.001,
		ReceiptID:      "rcpt-" + job,
		OutcomeJobID:   job,
		OutcomeVersion: 1,
	})
	if err != nil {
		// Same key rebound to a NON-VALID record (e.g. post-correction
		// recompute): fresh ACCEPTED evidence legitimately supersedes it.
		// A VALID record never rebounds (fail-closed conflict below).
		if errors.Is(err, reuse.ErrKeyConflict) {
			if art2, rerr := e.Store.Republish(key, reuse.PublishBody{
				ArtifactDigest: reuse.DigestBytes(out),
				Verification:   reuse.VerificationAccepted,
				EvidenceID:     "ev-" + job,
				VerifiedAt:     "2026-09-29T00:00:00Z",
				ActualCostUSD:  float64(n.CostUnits) * 0.001,
				ReceiptID:      "rcpt-" + job,
				OutcomeJobID:   job,
				OutcomeVersion: 1,
			}); rerr == nil {
				art = art2
				err = nil
			} else {
				err = rerr
			}
		}
	}
	if err != nil {
		// Includes ErrKeyConflict over VALID records: same key, different
		// bytes/evidence. Fail closed — never overwrite, never alias.
		rep.Resolutions[n.NodeID] = ResolveFailed
		rep.Reasons[n.NodeID] = "publish refused: " + err.Error()
		return
	}
	if err := e.Store.VerifyBytes(kd, out); err != nil {
		rep.Resolutions[n.NodeID] = ResolveFailed
		rep.Reasons[n.NodeID] = "binding check failed: " + err.Error()
		return
	}
	e.Bodies[art.ArtifactDigest] = out
	upArts[n.NodeID] = art.ArtifactDigest
	sink[n.NodeID] = out
	rep.ExecutionsOf[kd]++
	rep.Executed = append(rep.Executed, n.NodeID)
	rep.Resolutions[n.NodeID] = ResolveVerify
	rep.Reasons[n.NodeID] = "executed + verified + published"
	e.recordProgress(runID, NodeOutcome{NodeID: n.NodeID, KeyDigest: kd,
		ArtifactDigest: art.ArtifactDigest, OutcomeJobID: job, OutcomeVersion: 1, Verified: true})
}

// tryReuse attempts a verified reuse of key. Returns bytes on REUSE_VALID.
func (e *Executor) tryReuse(key reuse.ExecutionKey, reason string, rep *RunReport,
	upArts map[string]string, upBytes map[string][]byte, id string) bool {
	kd := key.KeyDigest()
	stored, hit := e.Store.Lookup(kd)
	if !hit {
		return false
	}
	dec := e.Store.Evaluate(key, reuse.LiveOf(key), e.lookup)
	if dec.Validity != reuse.ValidityValid {
		return false
	}
	b, ok := e.Bodies[stored.ArtifactDigest]
	if !ok {
		rep.Resolutions[id] = ResolveBlockedUnknown
		rep.Reasons[id] = "artifact bytes unavailable (fail-closed)"
		return true // resolved as blocked; do not execute blindly
	}
	if err := e.Store.VerifyBytes(kd, b); err != nil {
		rep.Resolutions[id] = ResolveBlockedUnknown
		rep.Reasons[id] = "binding check failed (fail-closed)"
		return true
	}
	upArts[id] = stored.ArtifactDigest
	upBytes[id] = b
	rep.Resolutions[id] = ResolveReuseValid
	rep.Reasons[id] = reason
	rep.Reused = append(rep.Reused, id)
	return true
}

// resolveP1: independent per-node store queries, fixed DAG, NO plan-level
// dedup memo: two occurrences of one ExecutionKey each execute on a miss.
// (In this executor each node id is visited once, so the P1/P2 difference is
// realized at the multi-output level: P1 runs each final output's plan
// separately; shared intermediates re-execute. See workloads multi-output.)
func (e *Executor) resolveP1(plan PhysicalPlan, n PlanNode, key reuse.ExecutionKey,
	upBytes map[string][]byte, runID string, rep *RunReport,
	upArts map[string]string, sink map[string][]byte) {
	if e.tryReuse(key, "P1 independent hit", rep, upArts, sink, n.NodeID) {
		return
	}
	if _, blocked := rep.Resolutions[n.NodeID]; blocked {
		return // tryReuse already resolved as blocked
	}
	e.executeNode(plan, n, key, upBytes, runID, TreatP1, rep, upArts, sink)
}

// resolveP2: DAG-aware — terminal-closure scheduling (outside-closure nodes are
// skipped in Run) plus an in-run shared-key memo (execute at most once).
// STALE/INVALID from tryReuse miss falls through to EXECUTE of the closure.
func (e *Executor) resolveP2(plan PhysicalPlan, n PlanNode, key reuse.ExecutionKey,
	upBytes map[string][]byte, runID string, rep *RunReport,
	upArts map[string]string, sink map[string][]byte) {
	if e.tryReuse(key, "P2 closure hit", rep, upArts, sink, n.NodeID) {
		return
	}
	if _, blocked := rep.Resolutions[n.NodeID]; blocked {
		return
	}
	kd := key.KeyDigest()
	if rep.ExecutionsOf[kd] > 0 {
		// Same key already executed this run (shared subgraph): reuse the
		// in-run result without re-executing. Find it via stored artifact.
		if stored, ok := e.Store.Lookup(kd); ok {
			if b, ok := e.Bodies[stored.ArtifactDigest]; ok {
				upArts[n.NodeID] = stored.ArtifactDigest
				sink[n.NodeID] = b
				rep.Resolutions[n.NodeID] = ResolveReuseValid
				rep.Reasons[n.NodeID] = "P2 shared-key memo (executed once)"
				rep.Reused = append(rep.Reused, n.NodeID)
				return
			}
		}
		rep.Resolutions[n.NodeID] = ResolveBlockedUnknown
		rep.Reasons[n.NodeID] = "shared key executed but bytes unavailable"
		return
	}
	e.executeNode(plan, n, key, upBytes, runID, TreatP2, rep, upArts, sink)
}

// finalize checks terminal acceptance: terminal verifier ACCEPTS final bytes.
func (e *Executor) finalize(plan PhysicalPlan, byID map[string]PlanNode, rep *RunReport,
	upBytes map[string][]byte, upArts map[string]string) {
	t, ok := byID[plan.Terminal]
	if !ok {
		return
	}
	fb, ok := upBytes[plan.Terminal]
	if !ok {
		rep.TerminalOK = false
		return
	}
	vf, ok := e.Reg.Verifiers[t.VerifierContract]
	if !ok {
		rep.TerminalOK = false
		return
	}
	// Terminal inputs for verification: upstream bytes.
	in := map[string][]byte{}
	for _, u := range t.Upstreams {
		if b, ok := upBytes[u]; ok {
			in[u] = b
		}
	}
	rep.TerminalOK = vf(t, in, fb)
	rep.FinalBytes = reuse.DigestBytes(fb)
	_, _, by := e.Store.GraphStats()
	rep.StoreWriteB = by
}
