# Correctness Release: Operations & Migration Notes

Companion to `docs/audit/CORRECTNESS_REPAIR_BASELINE.md`. Covers behavior and
contract changes introduced by PRs A–D that operators and integrators must know.

## Experiment reports (PR A) — verdict semantics changed

- Bootstrap comparisons now carry validity accounting
  (`valid_draws/invalid_draws/valid_fraction/invalid_reasons`). A conclusive
  verdict requires `valid_fraction >= --min-bootstrap-valid` (default 0.5).
  Reports that previously read CONCLUSIVE on sparse successes now read
  INCONCLUSIVE. This is intentional (survivorship bias fix), not a regression.
- Missing-cost sensitivity is adversarial: LOW favors the candidate, HIGH the
  baseline, with an explicit `missing_cost_basis` (`max-plausible-cost` vs
  `p90-bounded-sensitivity`) and bounded flag. Unbounded cases (missing
  costs, no configured upper) plus `--max-unmetered` breaches (default 0.10
  share) refuse conclusive verdicts (`missing-cost-*` gates).
- Censoring fraction now counts UNKNOWN + PENDING + unresolved matured jobs
  (was: UNKNOWN only). High-PENDING experiments trip the censoring gate.
- Allocation is enforced: nonuniform assignment probabilities, or mismatch
  with `--expected-weights`, fail the `allocation` gate (NOT_RANKABLE). The
  estimators remain unweighted — observational data still does not belong here.
- Quality floor is now also a visible gate (`quality-floor-*`), same verdict
  behavior as before.
- New CLI flags: `--min-bootstrap-valid`, `--max-unmetered` (0 = 0.10
  default; negative disables), `--max-cost`, `--expected-weights`. New report
  JSON fields are additive; old fields and their meanings are unchanged.
- Frozen charter items (floor value, window, bar, censor gate value) are
  untouched. The new gates add refusal paths; they do not change frozen
  thresholds.

## Policy and learning (PR B) — behavior changes

- `SelectWith` deep-copies arms and order, runs the strategy outside the
  lock, and errors on unknown-arm choices (previously returned silently).
  Custom strategies observe no behavior change except isolation.
- Observer callbacks now fire after the policy lock is released, with
  private score-map copies. Observers that relied on mutating the shared
  scores map will silently stop affecting the caller — that reliance was
  never contractual.
- `RecordOutcome` rejects non-finite reward weights; `Posterior.Observe`
  rejects non-finite/out-of-range Binarize thresholds; selection rejects
  non-finite UCB coefficients; nil RNG is rejected everywhere (`ErrNilRNG`).
  Previously these panicked or silently degenerated.
- Settlement attribution: single-attempt outcomes must name the selected
  arm; multi-attempt arms must be eligible; armless deciders allowed.
  Settlements that previously learned into unrelated arms now get 409 with
  ledger and policy untouched. This is a strictness increase by design.
- Learner arm-set contract: events for arms outside the genesis set fail
  with a Rebase hint; `Learner.Rebase()` adopts the current set explicitly.
  Rebuilds roll back state and cursor on fold failure.
- Rust gains `Error::InvalidParameter`; downstream exhaustive matches on
  `thompson_sampling::Error` need a new arm (additive, documented).

## Protocol (PR C) — wire migration

- Writers (both languages) now emit canonical protocol-v1 config JSON.
  Readers accept canonical plus: Go accepts the legacy PascalCase shape;
  Rust accepts absent `config` (default) and PascalCase arm/posterior
  aliases. Nothing previously readable became unreadable.
- `protocol/schema.json` rewritten to the canonical shape; `SPEC.md` line
  references corrected and canonical/migration documented.
- Float ULP note stands: compare snapshots with tolerance, never `==`.
- CI conformance now runs real tests (`-run TestProtocol`, replay test);
  the old patterns matched nothing.

## Operations (PR D) — deployment behavior changes

- Settlement listener binds loopback by default; non-loopback
  `SETTLE_ADDR` without `ALLOW_PUBLIC_SETTLE=1` refuses startup.
- New env: `MAPPER` (binary|noop), `SELECTION_SEED` (omit in production),
  `CHECKPOINT_INTERVAL` (0 disables ticker; shutdown checkpoint still runs).
- Graceful shutdown: SIGTERM/SIGINT drain both listeners (10s deadline),
  write a final checkpoint, then release stores. Bind failures are fatal
  (no half-alive gateway).
- `/health` returns 503 within a minute of a request-path persistence
  failure; validation rejections do not affect health.
- Evidence writer is single-writer flock-guarded like the ledgers.
- Control plane: unknown `STORAGE` (including `s3`) refuses startup;
  tenant handlers deny on missing auth context (defense in depth behind
  the middleware); `/metrics` stays public by design (tenant/arm IDs and
  means visible — network controls required; documented in code).
- Experiment resume binds to experiment ID, workload version, seed,
  charter digest, treatment config, and clock; mismatches refuse.
  Headerless (legacy) progress logs are refused — they predate binding.
- Env-mutating control-plane tests are now serialized; historical flakes
  should be gone (5/5 green runs observed).
