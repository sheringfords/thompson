# PR 3B Experiment Charter (FROZEN — see rules below)

This charter freezes every analysis parameter for the first randomized
experiment (PR3B_EXPERIMENT_SPEC.md). Amending any frozen value restarts the
experiment: open a new charter revision, re-tag, and discard prior
assignments from the analysis window (ledgers are kept for audit, never
reused).

## Frozen parameters

| Parameter | Value |
|---|---|
| Quality floor | validator pass rate ≥ 0.95 on held-out audit sample; ACCEPTED below floor ≠ success; winner accept-rate gate enforced in report |
| Outcome-maturation window M | 24h, common to all treatments (`--maturation 24h`; configurable in code, frozen here) |
| Collection window | stop when BOTH ≥7 days elapsed AND ≥1,000 matured jobs per treatment; hard cap 21 days |
| Randomization | per-job, concurrent, weights 1/3–1/3–1/3, stratified by traffic class; assigner seed recorded in every assignment row |
| Censoring gate | NOT_RANKABLE if any treatment's `censored_fraction` > 0.05 |
| Interim looks | at most 2, with O'Brien-Fleming-type alpha spending declared in the analysis log before the first look; unplanned peeking invalidates the CIs |
| Commercial bar (provisional) | T2 beats T0 **and** T1 by ≥15% relative with full 95% CIs below zero + surviving sensitivities; requires partner sign-off before any commercial claim |

## Sample-size basis (assumptions, NOT historical data)

No historical workload data exists for this system. Sizing uses the
two-sample normal approximation
n = 2σ²(z_{1−α/2} + z_{power})²/δ² with α = 0.05, power = 0.8, implemented
and unit-tested as `harness.RequiredPerGroup`:

| Assumed baseline mean cost/success | Assumed CV | σ | δ (15%) | n/group |
|---|---|---|---|---|
| $0.050 (illustrative) | 1.0 | 0.050 | 0.0075 | ~697 |
| $0.050 (illustrative) | 1.2 | 0.060 | 0.0075 | ~1004 |

The 1,000-jobs-per-treatment minimum covers CV ≤ ~1.2 under these
illustrative means. **Rule:** after the first 200 matured jobs per
treatment, recompute n from the observed pooled variance; if the recomputed
n exceeds collected jobs by >2×, extend the collection window (same charter,
recorded amendment) or stop for futility — never shrink M, the floor, or the
bar to fit the data.

## Treatments and isolation (restated normatively)

T0 = customer's existing fixed routing; T1 = cheapest quality-qualified
static strategy (fixed arm list + bounded retries, no learning); T2 =
verified-mode Thompson gateway learning from settled outcomes only. Separate
policy objects, learner cursors, decision files, outcome files, and
checkpoints per treatment; one `strategy_id` each; assignment persisted
before execution; retries never re-randomize.

## Dry-run acceptance (before customer traffic)

1. Synthetic traffic through all three file sets; `exp-report` renders a
   verdict on clean data and refuses (exit 2, reason stated) on: immature
   data, empty root, censored fixture, shifted fixture.
2. Assignment balance within ±10 points of weights at n ≥ 300.
3. Full `go test -race ./...` green on the implementation commit.
