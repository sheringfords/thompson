# Runtime Architecture Findings (Phase 12)

## Is Job + ExecutionKey + VerifiedArtifact + PhysicalPlan + ExecutionState minimal?

Each carries a distinct, load-bearing role proven by deletion: without the
Job, triggers have no target identity; without ExecutionKeys, mid-run
invalidation has no exact boundary (the stale-object bug proved keys must be
rebuilt, not remembered); without VerifiedArtifacts, reuse across switches
has no evidence; without PhysicalPlans, there is nothing to switch between;
without ExecutionState (completed/effects/spent/switches), resume and audit
are impossible. Scaffolding clearly marked for removal: the trigger Schedule
(test feed — production would wire real authoritative event sources), the
EffectSink (test external world), oracle/frozen harnesses, JSONL state files
(replaceable by the journal backend from PR #28 — natural next integration,
not done here).

## Is Thompson Sampling still necessary? Which router abstractions demote?

The entire assay chain (reuse → plan → replan) uses zero bandit machinery:
selection is deterministic quoting over verified state. Sampling/routers are
unnecessary anywhere in this core. The router demotes to an OPTIONAL policy:
it may PROPOSE candidate physical plans (or arms-as-plans), but the runtime
needs only the candidate set + quotes + verifier contracts to choose safely.
Learning, if any, belongs in cost estimation, never in correctness.

## What is the primitive?

Verified execution with incremental structure: exact identities make
recomputation minimal, evidence makes reuse safe, deterministic quoting makes
adaptation auditable, effect scopes make real workflows plannable. "Adaptive"
describes it only insofar as adaptation is fully explained by committed
evidence — no autonomy, no judgment. No renames or production changes made.
