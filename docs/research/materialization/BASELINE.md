# Materialization Baseline (Phase 0)

Mission: `THOMPSON_MATERIALIZED_EXECUTION_ASSAY_V1`. Research-only. No merge,
no deploy. No prerequisite branch rewritten or force-pushed.

## Verified topology (fetched 2026-09-29)

- `origin/main`: `814d2b29f66274a36451e740909a4db0e76b26f2` (unchanged).
- PR #29 (`VerifiedArtifact V1`): OPEN at `4f699633ab1a82b2ad8a8239e180f3d2b3aa2512`.
- PR #30 (`ExecutionPlan DAG`): OPEN at `b99d001a6f50bebe469d6b018665b4a75e5ee153`.
- PR #31 (`Runtime replanning`): OPEN at `42d2120080a79cc9d6a925762c48d5c2f7e7d483`.
- Main protection: disabled (unchanged). No PRs merged.
- New branch `research/materialized-execution-assay-v1` from PR #31 head in
  isolated worktree `/Users/wira/Documents/thompson-material` (contains the
  full #29+#30+#31 stack). Unrelated worktrees preserved.
- Toolchain: `go1.27.1 darwin/amd64`; `go/go.mod` declares `go 1.22`.

## Combined regression (GREEN — assay may proceed)

- `go build ./...`: OK.
- `go/assay/reuse`, `go/assay/plan`, `go/assay/replan` (full suites): PASS.
- `go/assay/journal`, `go/gateway/journalstore`, `go/thompson`, `go/outcome`:
  PASS serially (`-count=1`). One combined parallel run showed a FAIL in the
  journal package: the known flaky `TestFaultKillMidCommit` under parallel
  load (documented since the reuse assay; passes in isolation and serially).
  Not a blocker; final verification re-runs serially.
- `go vet` on assay trees: clean (run with implementation).
