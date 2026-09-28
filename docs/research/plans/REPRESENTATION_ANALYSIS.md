# Representation Analysis (Phase 10)

Core question: did the DAG turn verified computational identity directly into
execution structure, or did we merely build another orchestration layer?

## Answer: identity became structure, with one honest seam

`NodeKey` derives ExecutionKeys from (plan structure + upstream digests) with
NO stored node→key authority: keys are recomputed on every run, resume and
quote from the same function. Cross-plan stability holds (same computation,
different plan → same key; tested). Plan-local node ids never leak into keys
(upstream edges name upstream KEY digests). Mappings introduced: nodeID→key is
a pure function (recomputed, never reconciled); key→artifact lives once in the
reuse store; run progress (nodeID→artifact digest) is ephemeral resume state,
not authority. No parallel authority requires manual reconciliation: deleting
progress files only costs re-execution, never correctness.

## Collapse accounting

One `Artifact` record already covered cache entry + verification + provenance
(reuse assay). The DAG adds: `PhysicalPlan` (structure), per-run `RunReport`
(decisions), progress file (resume). Removed vs a naive orchestrator: no
per-node cache keys, no invalidator callbacks, no provenance joins at reuse
time, no plan-specific adapters (executor is fully generic; verified by
building D1, D2 and fan plans with zero scheduler changes).

## Added vs removed

Added: ~1.1 kLOC (`plan.go`, `executor.go`, `resolve.go`, `planner.go`,
`workloads.go`) + tests, zero new dependencies, zero production files. Removed
(in the assay's scope): duplicate-key re-execution (P0 does 8, reuse does 7),
outside-closure execution, wrong-plan selection. The seam: `Quote` conservatively
over-estimates (prices full plan, executes closure) — audit-safe direction.

## Verdict

Structure, not orchestration layer: the DAG is a view over key identities
(edges = upstream-key bindings), and every scheduling decision reduces to
store validity + topology. Nothing in the executor could disagree with the
store without failing closed.
