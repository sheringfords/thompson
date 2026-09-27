# Release Consolidation Report (Phase 5)

## Current main SHA
`d7a33e6` — Thompson pilot readiness V1 (#19). Unchanged throughout this
mission (verified by fetch at start and end).

## Final integrated candidate
Branch `release/consolidation-v1` (to be pushed from this worktree):
`d7a33e6` + one commit, “Deterministic gate ordering in experiment
reports”. Tree delta vs main: **one 10-line fix** (sorted gate emission).

Rationale for a one-commit delta: file-level reconciliation proved
`main@d7a33e6` already contains the full reviewed aggregate — my prior
`release/integration-v1@797d3c9` has a byte-identical tree
(`bec42ba9`, verified both directions), merged via PR #24 (squash
`18b738b`) plus pilot #19. There was nothing left to integrate.

## PR status and merge sequence
| PR | State | Disposition |
|---|---|---|
| #19 pilot readiness | MERGED (d7a33e6) | — |
| #20 PR A | CLOSED superseded | content on main; nothing to merge |
| #21 PR B | MERGED | stacked-review bookkeeping |
| #22 PR C | MERGED | stacked-review bookkeeping |
| #23 PR D | MERGED | stacked-review bookkeeping |
| #24 release/integration | MERGED (18b738b squash) | carried the aggregate |
| This branch | UNMERGED, to push | single gate-ordering fix |

No merge sequence remains to execute. If the gate fix is accepted, it
merges as one commit on top of main; the stale `review/correctness-*`
branches must NOT be merged (verified: they would regress main).

## Integration-only commits retained
1. `63afe94` Deterministic gate ordering (the sole code delta in this
   mission; found by the crash/resume equivalence proof).

## Independent test results (this mission, candidate worktree)
- Go: `go test -race -count=1 ./...` — all 9 packages ok.
- `go vet ./...` clean; `gofmt -l` shows only pre-existing dirt.
- Go 1.22 (`GOTOOLCHAIN=go1.22.0`): vet clean; tests ok on all 7
  concerned packages.
- Rust: workspace green (lib 85, protocol 6, control-plane 15, sim 18+);
  `fmt --check` clean; clippy 0 warnings.
- Protocol: `TestProtocol*` (7) + `tests/protocol.rs` (6) green both ways.
- Trace replay: no `traces/` dir — workflow leg correctly no-ops; the
  referenced `TestLedgerFromFileGroupsPaired` passes in the gateway suite.
- Helm: no local binary (CI-owned); both charts referenced by CI lint step.
- Binary routing + authenticated settlement: router suite green.
- Attribution/duplicates/corrections/malformed/crash/assignment/bootstrap/
  cost/censoring/gate/pilot-config suites: all green (full list in suite
  output; zero failures).
- Toolchains: go1.27.1, rustc/cargo 1.90.0, GOTOOLCHAIN go1.22.0.

## Synthetic experiment reproducibility (this mission)
- Uninterrupted 300-job run (`seed 20260105`, selection-seed 777):
  300/300 jobs, verdict NOT_RANKABLE (missing-cost gate: ~16% unmetered
  on all treatments — the corrected evaluator refusing, as designed).
- Crash at job 100 (SIGKILL, no graceful checkpoint) + resume to
  completion: **byte-identical reports** (assignments 108/88/104,
  treatments, comparisons incl. CIs, verdict, reasons) after normalizing
  only the analysis-clock field.
- Artifacts (manifest w/ version, per-treatment decisions/outcomes/
  evidence/jobmaps, progress log, report.json/txt) preserved under
  /tmp/rc-dryrun/{run1,run2} for review. All results labeled SYNTHETIC;
  no commercial claim is made or implied.

## Unresolved blockers
1. **BLOCKED_FOR_CUSTOMER_DATA**: `main` has no branch protection
   (re-checked: API 404). Required human steps: enable required-PR +
   required-CI ruleset; require the conformance/race/Rust checks;
   then this decision flips to READY_FOR_REVIEW.
2. Auto-commit/push mechanism persists; the local pre-push hook is not
   repository protection. No contamination of this worktree occurred
   (verified clean status before/after; hook exit 1 re-verified).
3. Stale review branches (`review/correctness-*`, old
   `release/integration-v1`) should be deleted after sign-off to prevent
   accidental merges that would regress main.

## Suitability statements
- Historical-workload feasibility assessment: suitable — the pilot
  feasibility CLI, workload contract, and acceptance suites pass on the
  candidate; synthetic dry run exercises the full path.
- Customer-derived data: NOT YET SAFE — blocked solely on repository
  controls (blocker 1), not on code correctness.

## Precise next action requiring human approval
1. Enable branch protection on `main` (required PR review + required CI).
2. Merge this branch (`release/consolidation-v1`, one commit) via PR with
   a fresh full-suite run on the merge result.
3. Delete stale `review/*` branches.
