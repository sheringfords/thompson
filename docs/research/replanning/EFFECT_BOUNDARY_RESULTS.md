# Effect-Boundary Results (Phase 8)

All 8 scenarios PASS (`effects_test.go`).

| # | Scenario | Verified behavior |
|---|---|---|
| 1 | Replan before any effect | Pre-start trigger switches shape; effect applies exactly once |
| 2 | Replan after PURE node | Prefix reused; switch; sink count 1 |
| 3 | Replan after idempotent effect | Same effect key reused across switch; no double-apply (sink 1) |
| 4 | Irreversible + incompatible alt | `NoLegalSuffix` (history intact); compatible alt switches cleanly |
| 5 | UNKNOWN scope (timeout) | Effect plan illegal; pure alternative selected; sink empty |
| 6 | Alt assumes effect absent | Rejected with explicit "already committed" reason |
| 7 | Crash after commit, before replan record | Resume replays log; converges with clean run; effect once |
| 8 | Crash after replan record | Switch survives; suffix continues; history extends, never rewrites |

Required behaviors hold: history never rolled back (4, 6, sink counts);
UNKNOWN never optimized through (5); illegal alternatives rejected at quote
time with reasons (4, 6) and at op level in depth (guard errors); restart
reconstructs the same boundary and history (7, 8: resumed final == clean
final, switch sequences extend identically); safe failure preferred
(`NoLegalSuffix` over invalid plans).
