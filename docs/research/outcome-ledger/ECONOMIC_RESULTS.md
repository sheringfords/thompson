# Economic Results (Phase 8, analytical — no executable assay per Phase 5)

No measurements taken (measuring would require the killed assay's fixtures;
synthetic fixtures must not serve as demand evidence). Findings from audit:

## Which metrics need what (by source availability)

- Fully loaded cost per settled job: provider console + fee invoices +
  labor/time systems. Computable in a warehouse wherever time-tracking
  exists (ITSM yes, AP partially, support partially, sales partially,
  SWE reviewers partially). The gap is LABOR OBSERVABILITY, not a ledger.
- Cost per attempt / retry cost: provider + CI + fee sources. Computable.
- Correction rate / time-to-settlement / UNKNOWN rate: native state
  histories (reopens, regressions, reversals). Computable; UNKNOWN is simply
  "no terminal state yet" — no new machinery needed to represent it.
- Realized loss per failed job: observable only where money moves natively
  (refunds, chargebacks, duplicate payments). Elsewhere modeled — and a
  Thompson ledger would model it identically. No advantage.
- Provider/release comparison: valid only with stable job identity +
  outcome — available in the same warehouse. No advantage.

## Verdict

Every listed metric is either computable from existing sources or
unobservable to any system. None requires a new canonical record. Measured /
modeled / unavailable split (frozen here for any future revisit): measured =
token/fee/CI-minute costs, native state histories; modeled = labor effort,
downtime, counterfactual loss; unavailable = true causal attribution of
downstream loss to a specific attempt.
