# Journal Gateway Baseline (Phase 0)

## Verified state
- `origin/main`: `17d3868` (PR #27 merged; protection still OFF — 404).
- Branch `feat/journal-gateway-pilot-v1` at `17d3868`, clean.
- Main-checkout `M go/harness/report_test.go` preserved; all worktrees intact.
- Assay prototype reproduces from main (its committed tests are the proof).

## Pre-change suite
`go test -count=1 ./...`: 11/11 ok (PILOT_BASE_EXIT=0), including
`go/assay/journal`.

## Integration dependencies (all present on main)
- `SelectionPolicy` interface; `CostAwarePolicy` (RuleV3 + reserve loop).
- `DecisionStore` / `DecisionScanner` / `OutcomeStore` interfaces (file
  implementations); `SafetyStore` concrete with Append/Events/Failed/Close.
- `SafetyController` (Authorize/Reserve/ObserveSettlement/Suspend/Resume,
  operator endpoints, internal-mux mounting).
- Assay `journal` package: Open/schema/version, CommitDecision (with
  exploration atomicity), SettleOutcome, RecordSafety, RecordAssignment,
  Replay, VerifyExploration, checkpoints.
- exp-run: manifest treatments, per-treatment gateways, SafetyConfigs map,
  AfterJob hook, crash/resume + supervised e2e.

## Frozen methodology (from assay plan, unchanged)
Equivalence = byte-identical reconstructions + identical subsequent
decisions; refusals preserved; sync counts + bytes as architectural
metrics; wall times reported with machine caveats, never ranked alone.
