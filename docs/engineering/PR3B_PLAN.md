# PR 3B Implementation Plan (bounded; DESIGN→BUILD handoff)

Scope: make the experiment in PR3B_EXPERIMENT_SPEC.md executable. The
verified learner (`go/outcome`), sampling math, and reward mapping are
**unchanged**. No dashboard, no hosted gateway replacement, no new reward
formula, no benchmark. Includes review fixes F1 (mount settlement) and F2
(execution-marker Seq) from PR3A_REVIEW.md.

## Work items (in order; each independently testable)

### 1. Review fixes F1 + F2 (prerequisite, ~½ day)

- **F1 — mount settlement.** `go/router/main.go`: add an internal listener
  (env `SETTLE_ADDR`, default `127.0.0.1:8081`) routing `POST /v1/outcomes`
  to `Router.SettleHandler` with a bearer-token `SettleAuth` (env
  `SETTLE_TOKEN`, constant-time compare). Public listener unchanged.
  Mode selection via env `ROUTER_MODE=legacy|verified` (default legacy);
  verified requires `DECISIONS_PATH`, `OUTCOMES_PATH`, `SETTLE_TOKEN` or
  refuse startup. Tests: startup-refusal matrix (missing env × mode);
  handler reachable on internal addr, unreachable on public addr
  (httptest both listeners).
- **F2 — execution-marker sequence.** `go/gateway/decision_store.go`:
  replace `DecisionExecution.Seq` (global, colliding) with per-decision
  monotonic `N` (1, 2, … per decision_id). Update recover paths (file +
  memory) and `decision_store_test.go` assertions. Pre-pilot schema change;
  no migration needed (no production ledger exists).

### 2. Treatment harness (new, ~2 days)

- **Module** `go/harness/treatment.go` (new file; existing `harness.go`
  untouched): `Assigner{Seed, Weights, Strata}` — deterministic per-`job_id`
  treatment draw (hash, not RNG-stream, so assignment is replayable);
  `Treatment{ID strategy_id, Dirs, Router}` — three independent
  verified-mode routers (T0 fixed = Thompson policy with updates disabled?
  No — T0/T1 are *static strategies*: implement as verified routers whose
  settlement is recorded but whose learner is a no-op sink? Cleaner: T0/T1
  run `legacy` routers (static behavior = current fixed routing) with
  decision+outcome files for accounting, while T2 runs verified+learning.
  Static arms for T0 = customer policy; T1 = cheapest-satisfying config.
  All three persist decisions + accept settlements identically, so
  accounting code is shared; only T2 learns.)
- **APIs:** `Assign(jobID, strata) (treatmentID, prob)`; `RouterFor(treatment)`;
  metric job `go/cmd/exp-report/main.go` (new CLI): reads the three file
  sets + assignment table, applies maturation window M, emits
  full-accounting table + primary metric + bootstrap CIs + the four
  sensitivity analyses + gate verdicts (RANKABLE/NOT_RANKABLE + reasons).
- **Tests:** assignment determinism + weight calibration (χ² smoke);
  treatment file isolation (no shared policy/cursor/inode — assert distinct
  paths + distinct policy pointers); metric job on synthetic fixture with
  hand-computed expected values (one ACCEPTED/REJECTED/UNKNOWN/PENDING/
  missing-cost/unmetered job per treatment); sensitivity analyses flip the
  verdict exactly when constructed to; gate trips on censored_fraction >
  0.05 fixture.

### 3. Charter + freeze ceremony (process, 0 code)

- Fill §6 table (floor, M, window, assignment, gates, alpha-spend) with the
  customer; commit as `docs/engineering/PR3B_CHARTER.md`; tag the commit.
  CI check (optional, cheap): a test that fails if charter values differ
  from spec defaults without an explicit override record. Any charter edit
  restarts the experiment (enforced by process + tag).

### 4. Dry run (validation, no customer traffic)

- Shadow/duplicate traffic through all three treatments; verify: assignment
  balance, settlement flow, maturation cutoff behavior, report renders with
  gates passing on synthetic clean data and refusing on synthetic dirty
  data (high censoring, shift injection).

## Explicit non-goals (repeated for the implementer)

No cost-aware mapper; no dashboard; no multi-replica; no changes to
`go/outcome` learning semantics (additive helpers only, if any); no new
sampling code; no production rollout (that decision needs the §6 bar met).

## Acceptance

F1+F2 merged; harness + `exp-report` tested per §2; charter frozen and
tagged; dry run green on clean data and correctly refusing on dirty data;
PR 3A review A-items dispositioned (A3 dead code removed, A5 comment fixed —
trivial, fold into item 1).
