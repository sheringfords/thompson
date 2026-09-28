# Shared-Subgraph Results (Phase 6)

Source: `dedup_test.go`, matrix two-output rows.

## Findings

- Fan 1→3 consumers: shared `mid` executes exactly once under P0, P1 AND P2.
  Dedup of single-node-id diamonds comes from content-addressed store keys, not
  from DAG-awareness: the second lookup hits.
- Fan-30 root change: exact closure (root+mid+30 consumers+bundle = 33
  executions), `mid` recomputed exactly once, repopulating all branches.
- D1 two outputs (`attest`-plan then `summary`-plan): `normalize` executes once
  total under BOTH P1 and P2 (store-level dedup across runs).
- Predeclared duplicate-key pair (D2 `unit-setup`/`lint-setup`, distinct node
  ids, identical ExecutionKey): one execution per run under P1/P2 (second is a
  store hit); two executions under P0 (no reuse, literally). P2's in-run memo
  never fires first in sequential execution — belt-and-braces only.

## Metrics

| Metric | Value |
|---|---|
| Shared ops requested (fan-30 recompute) | 30 consumer + 1 mid |
| Shared ops actually executed | 30 + 1 (exactly once each) |
| Duplicate work avoided vs P0 | 1 duplicate setup exec per D2 run; cross-output normalize 2nd exec |
| Planning overhead | µs-scale resolution; fsync-bound publishes dominate (see Performance) |
| Final verification correctness | all ACCEPTED, oracle-equal |

## Honest attribution (RQ2)

Shared-subgraph dedup value lives in EXACT KEYS + the shared store, not in the
DAG layer: independent per-node lookups (P1) already execute shared keys once.
The DAG layer contributes closure scoping (skip outside-closure nodes: D1
summary under attest-terminal saves 1 node/2u vs P1) and blocking/ordering, not
dedup. A fancier DAG scheduler cannot beat `Lookup` on this axis.
