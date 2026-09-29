# Assay Decision (Phase 12)

Rule applied: the frozen charter from MODEL_V1.md, fixed before evaluation.
No thresholds moved, no scenarios added or dropped after results.

## Charter evaluation

Work clause (C ≥15% fewer total measured work units than B on ≥2 of
{1%, 5%, schema-change, enrichment-change}): measured −60%, −20%, −5%, −6%
(C SLOWER in all four). **0/4 — FAIL.** Correctness clauses (zero incorrect
C terminals, zero C false reuse): PASS on all 10 mutations at N=100/1000.

## Decision: KEEP_VERIFIED_ARTIFACT_ONLY (with offline analyzer retained)

- Not CONTINUE: the work clause failed decisively; the graph (Bp→C delta)
  costs six figures of microseconds while eliminating zero additional
  operations beyond flat exact keys. A materialization library is not justified.
- Not STOP: its extra conditions do not hold — declaration burden stayed flat
  dep lists (no workload-specific invalidators written), and product
  boundaries held with a clean sibling vocabulary. There IS something to keep.
- What is kept: the evidence-bound reuse discipline (contract currency +
  outcome lineage per reuse) — B demonstrably lacks it (519 stale-contract +
  1 revoked-evidence false reuses that C/Bp refuse), and the offline analyzer,
  which independently reproduces the verdict from history (standalone tool
  value for execution-economics without runtime machinery).
- Explicitly NOT kept: dependency-graph runtime materialization as a work
  optimizer, and physical-plan selection (D added no systematic value; the
  choice was static).

## Negative findings retained

B≈Bp≈C≈D execution closures on every sparse scenario; C loses net work
through ~25µs/node validity machinery vs ~2µs/node cache lookups; contract
rotation makes C re-execute what B re-verifies (519 nodes, reported
conservatism); D never flips usefully; undeclared deps blind B and C alike.

## Smallest justified next engineering task

A VERIFY-resolution prototype scoped strictly to the artifact primitive:
re-verify byte-identical outputs under a rotated contract without
re-execution (the single measured waste with a correctness-preserving fix).
No graph, DAG, planner, or runtime work. Proposed only.
