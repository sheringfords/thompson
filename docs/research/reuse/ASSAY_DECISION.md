# Assay Decision (Phase 9)

Rule applied without moving thresholds (gates fixed in the mission).

## Gate results

1. Zero false reuse on held-out controlled workloads: PASS (B2: 0 false, 0 bad
   invalidation, 28 correct reuses over 126 held-out runs; dev identical shape).
2. Dependency invalidation correct and reproducible: PASS (all one-at-a-time
   mutations → STALE with named dep; transitive A→B→C; restore semantics; replay
   deterministic; seeds frozen).
3. Materially more work eliminated than overhead: PASS with stated bounds —
   R≥2 wins at modeled costs ($0.50/$0.20); per-hit machinery 49 µs (~$7e-10 at
   assumed $0.05/h); R=1 negative case measured and reported.
4. Genuine state/machinery reduction: PASS (3 roles → 1 record + bytes; generic
   engine, flat dep lists, no per-workload invalidators; zero new deps).
5. Both workload classes benefit without workload-specific architecture changes:
   PASS (same `Store`/`Evaluate` for W1 and W2; only key-construction fields differ).

## Decision: CONTINUE_TO_EXECUTION_PLAN_ASSAY

All five CONTINUE conditions hold on controlled workloads. Scope of the claim is
deliberate: exact dependencies + independently verifiable computations only. No
semantic caching, no universal incremental computation claimed.

## Retained negative results / limitations

- Naive input-keyed caching is incorrect, not merely slower: 55–77 false reuses
  per corpus, including reuse of authoritatively REVOKED results.
- Prototype commit path is fsync-bound (~6 ms/record; invalidation ~5–18 ms/edge).
  Fine for validation/extraction cadences; write-heavy workloads need group-commit
  (future work, not this assay).
- 1M-record row is a labeled projection, not a measurement.
- Binding constraint: consumers must honestly enumerate correctness deps. Where
  deps cannot be enumerated, this primitive does not apply (STOP condition respected).
- Modeled, not wall-clock, execution costs; Linux measurements not taken (darwin only).

## Smallest next experiment

ExecutionPlan assay: a planned DAG of ExecutionKeys with shared-subgraph dedup —
measure coordination reduction on a 3–5 stage pipeline (validate → extract →
aggregate → report) reusing this store unchanged. Proposed only, not implemented.
