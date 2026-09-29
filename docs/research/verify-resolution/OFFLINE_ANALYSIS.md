# Offline Analysis (Phase 12, P1)

Source: `offline.go`, `TestOfflineAnalyzerUnit` (`testdata/vr_offline.json`).
Given recorded resolutions, the analyzer counts what conservative coupling
would have reproduced unnecessarily (every VERIFY row's production cost) and
separates verification-only churn (claim rows per contract) from computation
churn (RECOMPUTE rows). Contract ranking identifies where re-verification
capacity pays most. Deterministic and machine-readable; no execution.

On assay history the analyzer attributes the materialization assay's
contract-rotation waste (519-node re-executions) to verification-only churn —
the exact category this assay eliminates — quantifying the retained analyzer
value without any runtime machinery.
