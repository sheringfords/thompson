# Release Reconciliation (Phase 1)

## Method
Compared commit graphs (`merge-base --is-ancestor`, `gh` PR states) and tree
content (`rev-parse <ref>^{tree}`, empty `git diff`) across `origin/main`,
`origin/release/integration-v1`, and `origin/review/correctness-{a,b,c,d}`.

## Proven result
`origin/main@d7a33e6` and the reviewed integration tip `797d3c9` have
**byte-identical trees** (`bec42ba9`, empty diff both directions). No code
merge is required; the candidate IS main.

## How it happened (reconstructed)
- PR #24 was opened from `release/integration-v1@797d3c9` (the fully
  reviewed aggregate: A+B+C+D merges + pilot #19 + compatibility commits)
  and **squash-merged** into main as `18b738b`.
- PR #19 (pilot) merged on top as `d7a33e6` (current main tip).
- PRs #21/#22/#23 show MERGED (stacked-review bookkeeping); PR #20 was
  closed as superseded after file-level verification. None require action.
- The old review branches' remaining diffs vs main are stale duplicates
  (older variants of code main carries newer); merging any of them would
  REGRESS main. They should be left alone or deleted, never merged.

## Reconciliation table (source → disposition on main@d7a33e6)
| Source | Status | Evidence |
|---|---|---|
| PR #20 (PR A: stats) | CLOSED, content on main | markers `bootstrapCompare`, `RelCILow`, `checkAllocation` present; strict-gate tests pass on main |
| PR #21 (PR B: policy/learning) | MERGED, content on main | markers `armInGenesisLocked`, `checkAttribution`, `ErrNilRNG`, `InvalidParameter` present |
| PR #22 (PR C: protocol/CI) | MERGED, content on main | `config_json.go`, 5 fixtures, `TestProtocol*` + `tests/protocol.rs` present |
| PR #23 (PR D: runner/ops) | MERGED, content on main | `storage_from_kind` + tests, `NoopMapper`, `X-Selected-Arm`, `ALLOW_PUBLIC_SETTLE` present |
| PR #19 (pilot) | MERGED as d7a33e6 | feasibility, workload_contract, pilot_config + tests present |
| PR #24 (my aggregate) | MERGED as 18b738b (squash) | tree equality proven |
| Integration fixes (openTestRunner variadic, STORAGE seam, fatal listeners, unique ports) | On main | verified by grep + green suites below |
| My `release/consolidation-v1` + empty merge `6fa0385` | Redundant | tree-identical to main; retire, do not merge |

## Merge method chosen
Neither: no merge is needed or permitted (would be a no-op at best). The
candidate for validation is `origin/main@d7a33e6` itself, checked out fresh
in `~/Documents/thompson-release-candidate`. No history was rewritten; no
force-push occurred.
