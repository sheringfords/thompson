# Offline Analyzer (Phase 8)

Source: `analyzer.go` + `TestOfflineAnalyzer[_RealHistory]`
(`testdata/mat_analyzer.json`). Research-only; operates on recorded history;
deterministic (byte-identical across runs); no causal production claims.

## Interface (as proposed)

- `analyze(history)` → opportunity (avoidable work per mutation), stale-work
  rate (closure fraction), closure sizes, break-even verdicts,
  plan counterfactuals, reuse frequencies.
- `materialization_opportunity` ranks mutations by avoidable work
  (no-change tops: everything reusable; full-replace offers nothing).
- `recomputation_frontier` returns per-mutation closure sizes (what must
  recompute under C).
- `break_even` reports repayment: "never at current overhead" for no-change
  at measured constants (C work ≥ B work) — the analyzer independently
  reproduces the Phase 5 verdict from history alone.
- `plan_counterfactuals` records staged-vs-direct remaining-work estimates
  (D rows answer directly; staged rows mark −1 with the reason recorded).

## Finding

Historical analysis reliably identifies WHERE recomputation concentrates
(shard/global nodes, contract rotations, full replacements) and WHAT it costs
— the analyzer has standalone value for offline execution-economics even
where runtime materialization overhead is unattractive. This is the evidence
base for a KEEP_OFFLINE_ANALYZER_ONLY fallback, decided in ASSAY_DECISION.
