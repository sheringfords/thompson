# Verify-Resolution Baseline (Phase 0)

Mission: `THOMPSON_VERIFY_RESOLUTION_ASSAY_V1`. Research-only. No merge, no
deploy. No prerequisite branch rewritten or force-pushed. V1 semantics frozen.

## Verified topology (fetched 2026-09-29)

- `origin/main`: `814d2b29f66274a36451e740909a4db0e76b26f2` (unchanged).
- PR #29 (artifact V1): OPEN `4f69963`, PR #30 (plan DAG): OPEN `b99d001`,
  PR #31 (replanning): OPEN `42d2120`, PR #32 (materialization): OPEN
  `794d7ce` (all MERGEABLE, none merged).
- New branch `research/verify-resolution-assay-v1` from PR #32 head in
  isolated worktree `/Users/wira/Documents/thompson-verify` (full stack).
  Unrelated worktrees preserved.
- Toolchain: `go1.27.1 darwin/amd64`; `go/go.mod` declares `go 1.22`.

## Combined regression (GREEN — assay may proceed)

- `go build ./...`: OK (clean-checkout posture: the EconRow defect class from
  the materialization assay is checked explicitly on every new package).
- `go vet` on reuse/plan/replan/materialization: clean.
- V1 regressions (`go/assay/reuse` canonical/publish/correction/crash): PASS.
- Materialization (`TestAdv`, `TestOfflineAnalyzer`): PASS.
- Plan (`TestTreatmentMatrix`, `TestCycle`), replan (mid-run invalidation,
  held-out), journal, journalstore, thompson, outcome: PASS, `-count=1`.
