# Minimal Recomputation Results (Phase 5)

Source: `treatments_test.go`, `matrix_test.go` (`testdata/plan_matrix.json`),
held-out gate. All rows: terminal ACCEPTED + byte-equal to same-plan P0.

## Closure proof (executed sets, warm store)

- D2 lintcfg change → exactly `{lint, aggregate, attest}` (8u of 37u).
- D2 toolchain change → compile closure `{compile, lint-setup, unit-setup,
  unit, lint, aggregate, attest}`, `identity` reused.
- D1 summary op-version change → `{summary}` only (P1) / `{}` + skip (P2,
  summary outside attest closure).
- D1 report-contract change → report closure recomputed (covered by contained
  variant on summary-terminal plan: exactly `{summary}`).
- Irrelevant (job-id-only) change → 0 executions, full reuse.
- Repeat identical → 0 executions.
- Cold D2 P2 → 7 executions + 1 dedup reuse (P0 executes 8: even cold, the
  duplicate setup key executes once under reuse treatments, twice under P0).

## Hard gates (all PASS)

- Zero incorrect final artifacts (every row `final_eq_same_plan_p0`, held-out
  seeds 1001–1003 × D1 × D2 × P1/P2/P3).
- Zero consumption of STALE/INVALID/UNKNOWN (tampered-bytes test: no reuse,
  terminal rejected; ghost-outcome test: never VALID).
- Zero duplicate execution of one shared key per run (`ExecutionsOf ≤ 1`
  everywhere; fan-30 mid exactly once).
- Every executed node in the required closure or selected alternative
  (matrix `WorkUnits` + per-test exact-set assertions).

## Correction, restore, crash

- Extract-outcome correction → extract recomputed via republication
  (`republished after INVALID`, history preserved), downstream closure
  recomputed, `validate` reused, final == oracle.
- Restore exact key + authoritative source → VALID reuse; + corrected source →
  stays INVALID (reuse-assay gate, unchanged).
- Crash after 2 nodes → resume completes, 7 progress records, no duplicate
  execution, final == oracle. Crash after shared fan node → mid not re-executed.
