# Adversarial Results (Phase 8)

Source: `adversarial_test.go` — all 12 scenarios PASS.

| # | Scenario | Behavior (verified) |
|---|---|---|
| 1 | Cheapest plan has STALE dep | Priced as execute (identity 0u, lint 6u); re-executed; final oracle-equal |
| 2 | Cheapest plan has INVALID artifact | Recomputed via republication; downstream closure rebuilt; accepted |
| 3 | Bytes fail digest check | Zero reuse; terminal rejected; nothing downstream consumes |
| 4 | UNKNOWN dependency | Never VALID, never a shortcut; failure recorded, terminal rejected |
| 5 | Cost tie | Same plan 3/3 runs (lexicographic); reason recorded |
| 6 | Missing cost estimate | Plan illegal; falls back to legal alternative; all-illegal → refusal error |
| 7 | Shared node fails verification | FAILED; all downstream BLOCKED; terminal rejected |
| 8 | Direct illegal, staged legal | P3 selects staged with recorded reason |
| 9 | Cycle | Quote illegal + Run refuses before any execution |
| 10 | Duplicate node id | Refused before execution (conflicting or identical) |
| 11 | Crash after shared node | Resume reuses it; zero re-execution; final oracle-equal |
| 12 | Fan-30 invalidation | Exact 33-node closure; mid exactly once |

Required behaviors hold: correctness dominates cost (1,2,7,8); UNKNOWN never a
shortcut (3,4); invalid plans refused pre-execution (6,9,10, planner op/contract
registration checks); determinism under equal inputs (5); every selection
records why (Quote.Reason on all P3 rows).
