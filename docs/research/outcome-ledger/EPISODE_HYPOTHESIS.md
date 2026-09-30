# Economic Episode Hypothesis (Phase 1 — hypothesis only, not product truth)

A cross-system economic episode for one delegated business job:

- **Job**: stable business identity (e.g. ticket ID, invoice ID, incident ID,
  opportunity ID, PR/issue ID), requested intent, request timestamp,
  references into owning systems of record.
- **Attempt**: agent/release/provider/model identity where available,
  start/end, execution result, retry/fallback links, observable direct cost.
- **CostEvent**: inference, tool/provider fees, retries, human review, manual
  correction, other attributable cost — each sourced, none imputed silently.
- **OutcomeObservation**: source system, observed state, timestamp, authority
  level (execution telemetry ≠ business authority).
- **Settlement**: ACCEPTED | REJECTED | UNKNOWN + settled_at + settlement
  authority + reason. Delayed by nature; UNKNOWN is terminal until authority
  exists.
- **Correction**: prior settlement → new settlement + authoritative source +
  timestamp + reason. Appends; never rewrites.
- **EconomicResult**: fully loaded attributable cost; realized value/loss
  ONLY where a source system observably contains it (never invented).

## Invariants

HTTP/model/tool success is not business success. UNKNOWN is legitimate.
Corrections append. No LLM opinion becomes authoritative by convenience.
Incumbents remain authoritative for facts they genuinely own. The candidate
ledger must not copy mutable business state just to become another database —
it may only own what no source system can hold (tested in Phases 3–4, 10).
