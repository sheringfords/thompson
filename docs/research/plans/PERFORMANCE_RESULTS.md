# Performance Results (Phase 7)

Source: `performance_test.go` (`testdata/plan_perf.json`), matrix work-units.
Primary metric: total executed work per independently verified final artifact
(measured synthetic CPU + node counts, not token claims).

## Measured (darwin, 5 trials, synthetic SHA-256 work)

| Case | Exec | Reused | Op work | Overhead | Latency | Store |
|---|---|---|---|---|---|---|
| D2/P0 cold | 8.0 | 0 | 251.6ms | 0.6ms | 252.3ms | 0B |
| D2/P2 cold | 7.0 | 1.0 | 202.0ms | 64.0ms | 266.1ms | 11.9KB |
| D1/P3 cold (→direct) | 3.0 | 0 | 150.3ms | 26.8ms | 177.2ms | 4.7KB |

## Readings

- Real work dominates: per-unit CPU ≈ 7–8ms; planner/lookup/evaluate ≈ µs/node.
- `PlanOverheadNS` INCLUDES fsync-bound publishes (~6–9ms/record macOS):
  cold P2 is wall-slower than P0 (266 vs 252ms) while executing less CPU work
  (202 vs 252ms). Durability is paid on write, repaid on reuse — cold runs do
  not win wall-clock; R≥2-equivalent repetition does.
- P3 cold D1: 3 exec/20u (direct) vs staged 7 exec/23u — selection value is
  real even cold. Warm staged: 0 exec.
- Matrix work-units (deterministic, no timing noise): D2 lintcfg P1/P2 = 8u of
  37u (78% avoided); D1 warm P2 = 0u; D2 tree-change = full re-execution in
  both (26u, unavoidable — inputs changed).
- Store bytes: ~1.6KB/record (unchanged from reuse assay); cold+warm latency
  measured per verified final artifact in JSON.

## Negative cases

- No-repeat workloads: reuse machinery + fsyncs are pure overhead (P0 wins).
- P1 == P2 executed-work on D2 (all scenarios): DAG layer adds audit/closure
  value, not work reduction, where plans have no outside-closure nodes.
- Quote/execution asymmetry: Quote prices ALL plan nodes; P2 executes only the
  terminal closure (D1 warm quote 3u, execution 0u). Conservative, documented;
  never incorrect (over-estimate only).
