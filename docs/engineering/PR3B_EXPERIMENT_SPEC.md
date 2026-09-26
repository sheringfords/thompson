# PR 3B Experiment Spec — revised (DESIGN ONLY)

Revision notes vs the first draft: adds a common outcome-maturation window
(§2), full-assignment accounting with accumulated costs (§3), explicit
UNKNOWN/missing-cost handling plus sensitivity analyses (§4), a frozen
floor/stopping rule with a minimum economic effect (§6), and hardened
randomization/state-independence requirements (§5). The cost-blind vs
offline-evaluation distinction (§7) is preserved and sharpened.

## 1. Question and arms (unchanged)

Does adaptive Thompson routing lower the **fully loaded cost per verified
successful job at an agreed quality floor** versus static alternatives?

- **T0 fixed:** the customer's existing static routing policy (quality + cost
  reference).
- **T1 cheapest-satisfying:** the cheapest static strategy meeting the
  quality threshold (cost-floor reference).
- **T2 thompson-adaptive:** the PR 3A verified-mode gateway learning from
  settled outcomes only.

## 2. Outcome-maturation window (new, binding)

Verification arrives late (judges, human review, reconciliation). A job
assigned at time *t* is evaluated on the ledger state at *t + M*, where **M
is the common outcome-maturation window, identical for all treatments**
(default proposal: M = 24h; freeze the value in §6). Rules:

- Only outcome versions with `verified_at ≤ t + M` count toward the primary
  metric. Later corrections are recorded and reported (with a
  `correction_received` marker) but do not rewrite a closed window.
- Jobs assigned in the last M of the collection window mature after close;
  analysis begins only after the final job matures. No partial-maturity
  analysis.
- PENDING at maturity = unresolved (see §4). UNKNOWN at maturity = censored.
- M is a property of the verification pipeline (slowest verifier + buffer),
  not of the treatments; per-treatment maturation differences are a
  data-quality finding, never an analysis adjustment.

## 3. Full-assignment accounting (new, binding)

The readout reports **every assigned job**, not just settled ones:

- Per treatment: assigned jobs; settled (ACCEPTED/REJECTED/UNKNOWN) at
  maturity; unresolved (PENDING) at maturity; withdrawals (jobs removed by
  cause outside the treatments, e.g. caller cancellation — reported with
  reason, never silently dropped).
- **Accumulated costs are reported for all assigned jobs**, including
  unresolved and withdrawn ones: Σ attempt costs + human-review costs as
  metered, with unmetered counts alongside. A treatment that burns cost on
  jobs that never settle must not look cheaper than one that settles them.
- The primary metric denominator counts only matured ACCEPTED jobs at or
  above the quality floor; the full-accounting table is published beside it
  so cost-shifting into unresolved jobs is visible.

## 4. UNKNOWN, missing costs, and sensitivity (new, binding)

- **UNKNOWN at maturity:** excluded from numerator and denominator of the
  primary metric; reported as `censored_fraction` overall, per treatment,
  and per arm. Gate: `censored_fraction > 0.05` in any treatment (tunable,
  frozen in §6) renders the comparison NOT_RANKABLE — heavy censoring is a
  data-quality failure, not a result.
- **Missing costs:** nulls propagate, never zero-fill (contract §1.5). Two
  readouts are published: (a) primary metric over fully-metered jobs with
  the metered share stated; (b) bounds over all jobs with missing costs
  assigned worst-case-per-treatment (sensitivity, not imputation).
- **Sensitivity analyses (required, pre-registered):**
  1. *Worst-case censoring:* all unresolved/UNKNOWN jobs in the leading
     treatment count as REJECTED at that treatment's 90th-percentile job
     cost; trailing treatments unchanged. The claimed win must survive.
  2. *Missing-cost bounds:* (a) above, plus the mirror (missing costs =
     zero) to show the interval the data actually supports.
  3. *Maturity stress:* recompute with M/2 and 2M; the ranking must be
     stable or the result is declared maturity-sensitive, not a win.
  4. *Late-correction watch:* corrections arriving after maturity are
     tallied per treatment; systematic correction asymmetry (>2:1 ratio with
     n≥20) invalidates the window and triggers re-maturation, not
     re-interpretation.

## 5. Randomization and treatment independence (hardened)

- **Unit:** one job (`job_id`), assigned concurrently across T0/T1/T2 for
  the whole collection window — never sequential eras (audit D2).
- **Assignment:** external randomizer, fixed probabilities (default
  1/3–1/3–1/3; frozen in §6), stratified by traffic class if the mix is
  heterogeneous. Log the assignment probability per job; the randomizer seed
  and assignment table are archived with the experiment.
- **Independent treatment state:** separate policy objects, learner cursors,
  decision files, and outcome files per treatment (shared files with
  `strategy_id` partitioning are **not** permitted — a partitioning bug
  would cross-contaminate treatments silently). No shared caches, no shared
  breaker state, no shared rate limits between treatments.
- **Overlap:** all treatments eligible for the same job mix; eligibility
  exclusions (e.g. a treatment that cannot serve a class) are logged per job
  and reported as an overlap gap, not patched by reweighting.
- **Analysis:** T2−T0 and T2−T1 differences with bootstrap 95% CIs clustered
  by session/customer grouping; IPS/SNIPS over the T2 ledger as a consistency
  check only; rankability-style refusal on failed overlap/precision/
  censoring gates. Shift monitoring per treatment; mid-window shift ⇒
  non-stationarity flag, never a winner declaration.

## 6. Frozen parameters and the commercial bar (new, binding)

The following are written into the experiment charter **before the first
assigned job**; changing any of them restarts the experiment:

| Parameter | Frozen value (proposal; customer to confirm) |
|---|---|
| Quality floor | validator pass rate ≥ 0.95 on held-out audit sample; ACCEPTED below floor ≠ success |
| Maturation window M | 24h |
| Collection window | stop when BOTH ≥7 days elapsed AND ≥1,000 matured jobs per treatment; hard cap 21 days (analyze matured jobs, record any shortfall as a validity caveat) |
| Assignment | 1/3–1/3–1/3, stratified by traffic class |
| Censoring gate | NOT_RANKABLE if any treatment's `censored_fraction` > 0.05 |
| Stopping | No early stopping except a pre-declared O'Brien-Fleming-style alpha-spend with ≤2 interim looks; peeking without it invalidates the CIs |

**Minimum observable economic improvement:** T2 must beat **both** T0 and T1
on the primary metric by **≥15% relative** with the **entire 95% CI below
zero difference** (i.e. the pessimistic bound still saves money), **and**
survive sensitivity analyses 1–3. Rationale: below ~15%, the saving does not
cover the operating cost of the adaptive system itself (verification
pipeline, ledger storage, on-call review of a learning system, periodic
re-validation) plus the risk margin of optimizing a live vendor mix. The 15%
figure is a default; the commercial decision-maker may raise it, never lower
it without recording why.

## 7. Cost-blind learning vs offline evaluation (preserved)

PR 3A's `BinaryStatusMapper` stays cost-blind: the bandit learns success
probability; cost is accounted offline. T2 therefore tests
success-learning + retry/fallback structure on cost — the shipped system, not
a hypothetical cost-aware bandit. A cost-aware mapper remains out of scope:
new estimand, needs contract amendment + counterfactual evaluation + a fresh
experiment. PR 3B implements no reward formula and no benchmark.

## 8. Minimal external interface (to be built in PR 3B; unchanged list)

1. `POST /route` (exists): `decision_id` + `job_id` (+ arm).
2. `POST /v1/outcomes` (exists): PENDING/UNKNOWN/ACCEPTED/REJECTED + tape +
   provenance. **Plus F1 from the PR 3A review: the handler must actually be
   mounted** (internal listener + auth) — currently implemented but unwired
   in `router/main.go`.
3. New in PR 3B only: randomizer, per-treatment file sets, offline metric
   job (assignment table + full-accounting + sensitivities + gates).

## 9. Entry criteria (PR 3A review status folded in)

PR 3A review verdict (PR3A_REVIEW.md): approve with F1 (mount settlement)
and F2 (execution-marker Seq) fixed — both land in the PR 3B implementation
plan, not in 3A's frozen range. Remaining entry gates: single-writer
deployment; provenance-labeled verification with measured censoring; frozen
§6 charter. If any fails, PR 3B does not start.
