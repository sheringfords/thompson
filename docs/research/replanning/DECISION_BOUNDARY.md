# Decision-Boundary Results (Phase 10)

Source: `boundary_test.go`. Anti-thrash is structural: all triggers due at
one node-visit apply in a single batch → one assessment; deterministic quotes;
event-ID dedup; moot triggers (nodes never visited post-switch) are RECORDED
as dropped, never silent.

- Trigger at selection: pre-start drain switches before any execution.
- Trigger pre-terminal on completed work: sunk cost → zero switches.
- Multiple triggers: batch assessment; post-switch moot triggers dropped visibly.
- Contradictory triggers: last write wins, replay-identical twice.
- Oscillation (7 alternating): 1 switch, deterministic replay.
- Cost ties: lexicographic, stable over 3 runs.
- No legal suffix after irreversible effect: explicit `NoLegalSuffix`
  (see boundary 4-negative).
- Fan-out (20 consumers): root rotation → spent 64 (11 abandoned + 53 exact
  closure), wasted 11, final == rotated-world oracle.
- 100 sequential replans: identical finals and switch histories across two
  runs; switches ≤ assessments; every trigger applied or dropped-recorded.

No sleeps, no damping constants, every transition explained by committed
evidence (switch records carry trigger + quotes + rejections).
