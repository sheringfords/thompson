# Execution Plan Contract V1 (Phase 1)

The VerifiedArtifact V1 contract (`docs/research/reuse/VERIFIED_ARTIFACT_CONTRACT_V1.md`)
is reused UNCHANGED. This document only composes it.

## Types

**LogicalJob**: `job_id`, canonical input digests, `required_final` (expected
terminal node id), `terminal_verifier` (contract digest). The *what*.

**PlanNode**: `node_id` (plan-local, edges only), `op` + `op_version`,
`inputs` (declared non-upstream deps as `(name, digest)`), `upstreams`
(node ids), `executor` identity, `env` digest, `verifier_contract` digest
(empty = no independent verification; terminal node must have one),
`policy_digest`, `cost_units` (measured/declared synthetic work units —
economics input, never correctness input).

**PhysicalPlan**: `plan_id`, `job_digest`, nodes, edges (derived from
`upstreams`, never inferred), `terminal`, `policy_digest`. A bounded DAG:
no loops, no dynamic control flow, no LLM-generated structure.

**NodeResolution**: `REUSE_VALID` (hit + VALID + bytes verified),
`EXECUTE` (miss, or STALE/INVALID requiring recomputation),
`VERIFY` (executed, awaiting terminal/independent check — recorded, not a reuse),
`BLOCKED_UNKNOWN` (fail-closed: required dep unresolvable or validity UNKNOWN),
`FAILED` (execution/verification failure, conflict, or refusal).

## Node → ExecutionKey (no second authority)

An executable node's key is DERIVED, never stored as authority:

- `Operation/Version` = node op identity; `Executor/Env/VerifierContract/Policy`
  = node fields; `InputDigest` = digest of the node's own canonical input digest list.
- Upstream edges become deps named `upstream:<upstream KEY digest>` carrying the
  upstream ARTIFACT digest (content-bound: identical upstream bytes ⇒ identical
  downstream key, even across plans). This reuses `reuse.UpstreamPrefix` exactly,
  so cross-plan dedup falls out of key equality and plan-local node ids never
  leak into keys.
- Plan identity is NOT in node keys (deliberate: enables cross-plan reuse).

## Scheduling rules

- Topological order only (deterministic Kahn's, lexicographic tie-break); cycles,
  unknown upstreams, and duplicate node ids with conflicting definitions are
  refused BEFORE execution.
- Resolution per node against the existing artifact store: VALID → reuse (bytes
  re-verified); STALE/INVALID → recompute downstream closure; UNKNOWN → node and
  all downstream BLOCKED (never consumed as valid).
- Shared ExecutionKeys execute AT MOST ONCE per plan run (in-run memo + store).
- Terminal success requires the terminal verifier to ACCEPT final bytes AND
  byte-equality with full-recomputation ground truth in the assay.
- Restart resumes from durable node-completion records; a crash never fabricates
  a completed node (records written only after verify + publish).

## Cost model

`cost_units` per node are measured synthetic CPU-work units (Phase 7: real,
labeled synthetic systems work — never sleeps, never token claims). The planner
may read costs; correctness never depends on them. Invariants: reused nodes honor
V1 unchanged; STALE/INVALID/UNKNOWN never consumed as valid; no silent dep
changes; UNKNOWN never becomes VALID; order respects edges.
