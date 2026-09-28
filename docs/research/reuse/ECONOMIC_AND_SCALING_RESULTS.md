# Economic and Scaling Results (Phase 7)

Source: `TestEconomicsAndScaling`, `testdata/reuse_economics.json` (raw),
comparison JSONs. Execution cost MODELED (W1 $0.50, W2 $0.20 — the assay models
cost, it does not sleep); machinery cost MEASURED (darwin, go1.27.1).
Time→$ uses stated ASSUMPTION $0.05/compute-hour, labeled everywhere.

## Required outputs (measured)

| Output | Value |
|---|---|
| Cost saved / correct reused artifact | $0.50 W1, $0.20 W2 (modeled execution avoided) |
| Fresh executions avoided | dev 10+10, held-out 14+14 (matrix); 100% of repeats at rate R (sweep) |
| Reuse hit rate (matrix) | B2: 15/50 W1, 15/40 W2 dev (hits only where validity holds) |
| Invalidation rate | all single-dep/verifier mutations → STALE; corrections → INVALID (100%) |
| False reuse rate | B2: 0 (dev + held-out). B1: 60% W1 / 62.5% W2 dev hits are false |
| Lookup latency | warm ~11–13 µs, cold (post-reopen) ~11–12 µs — flat 1k→10k (O(1) map) |
| Full hit overhead (steady state, no fsync) | ~49 µs/hit: key-build+digest 12, lookup 12, evaluate 13, bytes-verify 12 |
| Invalidation latency | fsync-bound: ~5 ms/edge chain, ~18 ms/edge star (per-record sync; traversal negligible) |
| Storage growth | 1611–1614 bytes/record, strictly linear (stable 1k→10k) |
| Break-even execution cost | R≥2 at modeled costs; machinery ≈ $7e-10/hit (assumed rate) — any execution dearer than ~2× that wins from the 2nd identical run |

## Repeat-rate sweep (W1)

R=1: net NEGATIVE (pure overhead, reported as required). R=2: +$0.50. R=100: +$49.50.
Hashing (key build + digests) ≈ 25% of per-hit overhead; evidence validation +
lookup ≈ 75%. Hashing time is dwarfed by modeled execution savings from R≥2.

## Scaling

Measured 1k (6.0 s publish) / 10k (62 s publish, fsync-bound ~6 ms/record, macOS);
lookup flat; replay 38 µs/record (10k in 382 ms). 1M row = labeled PROJECTION
(linear storage, O(1) lookup); 100k fsync-bound build not attempted in-test.
Negative case documented: at 6 ms/record commit cost, write-heavy/no-repeat
workloads lose — reuse pays only when hits outnumber commits (R≥2 rule), and
invalidation storms pay fsync per edge until group-commit exists.

## Cold vs warm

Cold (replay + lookup) ≈ warm (lookup) for latency; replay cost is one-time
38 µs/record. No warmup advantage beyond avoiding replay.
