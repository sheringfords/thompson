# Adversarial Results (Phase 9)

Source: `adversarial_test.go` — all 12 scenarios pass.

| # | Scenario | Behavior |
|---|---|---|
| 1 | Missing required dependency | Key validation refuses; live-missing dep → UNKNOWN, never VALID |
| 2 | Incorrect dep digest | STALE with named dep; recompute, never silent reuse |
| 3 | Omitted dep from key | Reuse is byte-stale vs fresh (limitation DEMONSTRATED, not hidden) |
| 4 | Verifier rotation | C re-executes (conservative); B 319–5019 evidence-false reuses; Bp 0 |
| 5 | Corrupted bytes | Digest check fails; consumption refused |
| 6 | Revocation | Transitive INVALID (2/2 nodes); history intact; 1-node recovery |
| 7 | Fan-out (taxonomy) | Exact closure 120+19; prefixes reuse; terminal accepted |
| 8 | Global (schema) | Exact closure 480+19; only validates reuse |
| 9 | Irrelevant metadata | Full reuse (job labels outside keys by design) |
| 10 | Revert to old digest | Reuse iff evidence still valid; INVALID after revocation |
| 11 | Cycle | Terminates (state-guarded propagation); per-record validity sound |
| 12 | Duplicate edges | Refused at construction and at publish |

Required behaviors hold: fail-closed where correctness is unestablishable
(1, 2, 5, 12); missing knowledge never becomes a hit (1); invalidation
provenance recorded (`ChangedDep`/reason on every transition); outputs equal
recomputation or explicit refusal throughout. Scenario 3 bounds the model:
declared-dependency identity is blind to undeclared inputs — for B and C
alike.
