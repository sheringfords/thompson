# Integrated Baseline (Phase 0)

Mission: `THOMPSON_EXECUTION_PLAN_ASSAY_V1`. Research-only. No merge, no deploy.
PR #29 is never rewritten: all new work lives on `research/execution-plan-assay-v1`.

## Verified topology (fetched 2026-09-29)

- `origin/main`: `814d2b29f66274a36451e740909a4db0e76b26f2`
  (`Pilot: SQLite journal backend… (#28)`).
- PR #28: MERGED 2026-09-28, head `c640415e091661a4b418702cb5b311794a817774`
  (SQLite journal gateway pilot). Confirmed merged content.
- PR #29: OPEN, head `4f699633ab1a82b2ad8a8239e180f3d2b3aa2512`, base `main`,
  `MERGEABLE`. Contains only the verified-artifact assay.
- Open PRs: only #29. Branch protection on `main`: none (404).
- Overlap check (`git diff --name-only` from pre-#28 main `17d3868`):
  PR #28 touches `docs/design/JOURNAL_GATEWAY_*`, `docs/engineering/JOURNAL_*`,
  `go/assay/journal/*`, `go/cmd/exp-run/*`, `go/gateway/journalstore/*`,
  `go/gateway/safety.go`, `go/router/*`. PR #29 touches only
  `docs/research/reuse/*` + `go/assay/reuse/*`. **Zero file overlap.**

## Integration strategy (non-destructive)

- New worktree `/Users/wira/Documents/thompson-plan`, new branch
  `research/execution-plan-assay-v1`, created from PR #29 head `4f69963`.
- Merged `origin/main` (`814d2b2`) into the NEW branch locally → merge commit
  `6144671`. Zero conflicts (disjoint file sets). PR #29 branch untouched.
- Toolchain: `go1.27.1 darwin/amd64`; `go/go.mod` declares `go 1.22`
  (new code stays 1.22-compatible). Unrelated worktrees preserved.

## Latent defect found and fixed (in new branch only)

`go build ./...` on the integrated state failed:
`go/assay/reuse/assay.go:264: undefined: liveOf` — non-test code called a helper
defined in `reuse_test.go` (tests passed; builds did not). This defect predates
the merge (it is inside PR #29's files, not a #28×#29 conflict). Fix, new branch
only: promoted the helper to `reuse.LiveOf` in `go/assay/reuse/workload.go`,
rewrote the three call sites. PR #29 history unchanged.

## Combined regression (gate: GREEN, may proceed)

- `go build ./...`: OK. `go vet ./assay/reuse/`: OK.
- PR #28 side: `go/assay/journal`, `go/gateway/journalstore`, `go/cmd/exp-run`
  (`TestManifest|TestJournal|TestSupervised`): PASS.
- Protocol side: `go/thompson`, `go/outcome`: PASS.
- PR #29 side: `go/assay/reuse` unit + `TestThreeModeComparisonDev` +
  `TestZeroFalseReuseHeldOut` (clean-checkout equivalent state): PASS.

No behavioral, package, dependency or test conflicts from the combination.
