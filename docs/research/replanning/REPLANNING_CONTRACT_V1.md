# Runtime Replanning Contract V1 (Phase 1)

LogicalJob, PhysicalPlan, PlanNode and VerifiedArtifact V1 are UNCHANGED.
This document adds execution-state, triggers, effects and suffix planning.

## ExecutionState (per run, durable)

`completed`: nodeID → {key digest, artifact digest, outcome job/version, effect
class + effect record}. `validArts`: currently VALID artifact bindings usable at
zero cost. `pending`: topo-ordered unfinished nodes of the ACTIVE plan.
`evidence`: observed authoritative events (trigger log). `spentUnits`: executed
work so far (reported; never makes an unsafe plan legal). `switches`: auditable
plan-switch history (old plan, new plan, trigger, quoted remaining cost each).

## ReplanTrigger (explicit authoritative events only)

`DEPENDENCY_INVALIDATED`, `ARTIFACT_BECAME_VALID`, `ARTIFACT_REVOKED`,
`VERIFICATION_FAILED`, `EXECUTION_COST_CHANGED`, `OPERATION_FAILED`,
`BUDGET_REDUCED`. Triggers arrive via assay hooks (test-injected authoritative
events, clock-free and deterministic), never inferred.

## Replanning boundary + RemainingPlan

The boundary splits COMPLETED (history, immutable) from REMAINING (replannable).
A RemainingPlan is a legal suffix over the ORIGINAL LogicalJob such that:
completed VALID artifacts satisfying its nodes are credited at zero cost;
completed STALE/INVALID/UNKNOWN nodes cannot satisfy anything; every node in
the suffix resolves against CURRENT authoritative state; the terminal still
requires ACCEPTED verification. Suffix legality is checked by the same
`Quote` machinery restricted to unfinished nodes (no future knowledge, no
held-out truth, no LLM, no learned policy, no manual overrides).

## Effect classes (Phase 2 binds these to ops)

- `PURE`: abandon/replace freely when unneeded.
- `IDEMPOTENT_EFFECT`: retry only under its explicit idempotency contract
  (same key ⇒ same effect record; duplicates detected, not double-applied).
- `IRREVERSIBLE_EFFECT`: hard historical boundary. Suffix candidates must be
  COMPATIBLE with the effect having occurred: any candidate whose correctness
  assumes the effect did not happen is rejected. No generic compensation.
  Ambiguous external effects are UNKNOWN (fail-closed, never optimized through).

HARD RULE: a replan never pretends a committed external effect did not occur.
History is append-only; switches are recorded, never rewritten.

## Determinism

Lowest quoted remaining cost wins; ties break by lexicographic plan ID, then
fewest unfinished nodes. Every switch records old/new plan, trigger and quotes
of all legal candidates + rejection reasons. Sunk cost is reported, never an
input to legality. Anti-thrash: at most one switch per trigger event; repeated
identical triggers do not re-switch (monotone switch counter + trigger dedup
by event id); 100 sequential replans must reproduce identical history.
