# Scaling Results (Phase 6)

Source: `TestScaling` (`testdata/mat_scaling.json`).

## Closure scaling (exec counts, B==C everywhere)

| N | 0% | 1% | 5% | 20% | 100% |
|---|---|---|---|---|---|
| 100 | 0/0 | 9/9 | 27/27 | 89/89 | 519/519 |
| 1000 | 0/0 | 63/63 | 216/216 | 769/769 | 5019/5019 |
| 10000 | 0/0 | 519/519 | — | — | 50019/50019 |

(B/C exec identical in every cell.) C approaches O(changed closure) exactly —
because B does too: content keys make BOTH minimal. The graph adds no
closure precision over flat exact keys on this workload shape.

## Time and storage

- Totals converge where execution dominates (full-replace N=10000: B 204.7s
  vs C 206.0s — C's key machinery ≈ 26µs/node over 50k nodes, amortized).
- Totals diverge where reuse dominates (no-change N=10000: B 119ms vs C
  1537ms — C pays validity evaluation per node with zero execution to
  amortize against).
- fsync latency variance (±ms × thousands of writes) dominates CLOSE
  comparisons (e.g. N=1000 chg-20pc flips sign between runs); conclusions
  rest on systematic effects only: per-node machinery (C ~25µs vs B ~2µs),
  verification skips, and exec ties.
- Storage: C ≈ 1.6KB/record steady-state (linear, stable 100→10000);
  B cache grows similarly per entry without validity metadata. Cold
  index/replay: C replays JSONL at ~38µs/record (reuse-assay figure,
  unchanged); B replays its own log similarly.
- N=10000 sampled to {0%,1%,100%} (documented time-box). No 100k/1M claims.

## RQ5 answer (when is the machinery justified)

C's cost is O(nodes)×~25µs per version regardless of change rate; its work
savings vs B are bounded by skipped terminal verifications (only full-reuse
versions) plus correctness envelope (unpriced). C pays off in work terms only
if per-version terminal verification exceeds ~25µs × node count — i.e.
terminal verification above ~25µs/record sustained. Below that, B wins every
cell; the graph is pure overhead.
