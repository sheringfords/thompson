# Correctness Repair Baseline (THOMPSON_CODEBASE_CORRECTNESS_V1, Phase 0)

- Base: `origin/main` at `abf2d0e` ("docs: restore flow diagram embed removed in #15 (#18)").
- Worktree: `~/Documents/thompson-correctness`, branch `fix/correctness-pra-statistical` (PR A work).
- Isolation: local pre-push hook blocks `main` (verified exit 1); server-side
  branch protection still DISABLED (human-owned blocker, re-checked this session).
- Coordination: the main checkout holds another agent's uncommitted pilot work
  (`go/cmd/exp-run/{main,feasibility,pilot_*}.go`, workload contracts, plus
  edits to `go/cmd/exp-run/main.go` and `go/harness/report_test.go`). This
  mission works exclusively in the worktree and does not touch those files
  outside it. Overlapping files for later PRs: `go/cmd/exp-run/main.go`,
  `go/harness/report_test.go` — merge conflicts with pilot work are possible
  and flagged in the final report.
- Full suite on clean base: initiated (`go test -race -count=1 ./...`);
  focused suites (gateway/thompson/outcome/harness/router/cmd) green at base
  per prior session records. Results appended when the run completes.

## Full-suite baseline result (recorded post-run)

`go test -race -count=1 ./...` on `abf2d0e`: all packages green EXCEPT one
pre-existing data race in `go/cmd/exp-run/cluster.go` (`SpawnGateway`
reads the child-stderr `bytes.Buffer` while the os/exec copier goroutine
writes; surfaces in `TestE2EMissingStorageFailsClosed`). This race predates
the mission (test scaffolding owned by the pilot-readiness track) and is
fixed in PR D with a mutex-guarded buffer. No other failures.

## P0 reproduction index (all reproduced against `abf2d0e` before fixing)

### P0-1. Missing-cost sensitivity is same-direction (PR A)
- File: `go/harness/report.go` (`missingCostBounds`, ~line 735).
- Repro: candidate `[3,3,unmetered]` vs baseline `[4,4,unmetered]`; old HIGH
  fills the baseline with its own p90, hiding the adversarial case.
- Expected: HIGH (candidate-high, baseline-low) can exceed the point estimate.
- Observed: HIGH computed candidate-high/baseline-high; tight bounds that
  cannot flip. Fix + asymmetric fixture in PR A.

### P0-2. Bootstrap drops invalid resamples silently (PR A)
- File: `go/harness/report.go` (`bootstrapDiff`, `bootstrapRel`).
- Repro: candidate with zero successes → every resample NaN → dropped →
  `(NaN, NaN)` with no accounting; sparse successes → CI over survivors only.
- Expected: validity counts, reasons, coverage rule.
- Observed: no accounting; conclusive verdicts possible on survivor bias
  (blocked in practice only by NaN propagation, not by any rule).

### P0-3. Assignment probabilities recorded, never used (PR A)
- File: `go/harness/report.go` (`pairDiff`, `primaryOf`); `Assignment.Probability`.
- Repro: nonuniform weights (e.g. 0.5/0.5 vs charter 1/3) analyzed without
  complaint or weighting.
- Expected: rejection (this release) or IPW estimators.
- Observed: silent unweighted analysis.

### P0-4. SelectWith exposes live mutable arms (PR B)
- File: `go/thompson/policy.go` (`SelectWith`, ~line 423).
- Repro (to be added as test in PR B): custom strategy mutates
  `arms["a"].Posterior` through the passed map; subsequent `Record` folds
  into corrupted state.
- Expected: defensive copies or immutable view.

### P0-5. Rust/Go snapshot wire incompatible (PR C)
- Files: `crates/thompson-sampling/src/policy.rs:503-510`,
  `go/thompson/policy.go:677-682`, `protocol/schema.json`.
- Repro (PR C): Go snapshot without `config` → Rust `from_json` decode
  error; enum shapes differ (tagged snake_case vs untagged PascalCase).
- Expected: one canonical protocol-v1 with bidirectional golden tests.

### P0-6. CI conformance/trace gates match zero tests (PR C)
- Files: `.github/workflows/ci.yml:58-59`, `trace-replay.yml`.
- Repro: `grep -rn "func TestConformance\|func TestTraceReplay" go/`
  returns nothing; `go test -run TestConformance ./...` exits 0 with
  "no tests to run". `cargo test --test stress` names a nonexistent target.
- Expected: CI fails on missing coverage.
