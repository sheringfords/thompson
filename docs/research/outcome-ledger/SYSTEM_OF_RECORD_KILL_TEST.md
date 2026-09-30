# System-of-Record Kill Test (Phase 3)

Kill rule applied aggressively: if the complete useful episode reconstructs
from one incumbent system plus ordinary telemetry without new settlement
semantics → NO_NEW_SYSTEM_OF_RECORD.

## Results

- SUPPORT: ticket events + CSAT + payments + usage exports reconstruct
  per-case resolution, cost, and correction-aware success. Settlement rule
  (solved ∧ good-CSAT ∧ no-reopen ∧ no-refund) is a warehouse view, not new
  semantics. → NO_NEW_SYSTEM_OF_RECORD.
- ITSM: the suite already IS the episode (state, SLA, labor, reopen).
  → NO_NEW_SYSTEM_OF_RECORD (strongest).
- AP: the ERP ledger plus approval workflow already govern posting,
  reversals, and audit. An agent attempt is fully inside governed approval.
  → NO_NEW_SYSTEM_OF_RECORD (by definition).
- SALES_OPS: stage history + closed-won + enrichment invoices + usage
  reconstruct everything knowable; the remainder is counterfactual loss no
  record can hold. → NO_NEW_SYSTEM_OF_RECORD.
- SOFTWARE_ENGINEERING: PR/checks/issues/timeline with state_reason +
  CI minutes + usage + incident tags reconstruct the settled episode; the
  settlement rule is a view. Closest call (production attribution is
  heuristic), but the heuristic binds Thompson equally — no Thompson-shaped
  advantage. → NO_NEW_SYSTEM_OF_RECORD.

## The five kill questions, answered

1. Incumbents can define final outcomes (solved/resolved/posted/won/merged
   plus native correction states) — yes, all five.
2. One warehouse query + trace/cost exports reconstruct the episode — yes,
   with ordinary joins on stable job IDs, in all five.
3. Incumbents could add missing fields trivially (agent-release tags, attempt
   links, custom objects) — yes; they own the identities involved.
4. Thompson would copy mutable business state (ticket/invoice/stage/PR
   status) while authority stays elsewhere — yes, unavoidable for any
   ledger-shaped proposal here.
5. Without copying, Thompson is foreign IDs plus joins — yes: job IDs,
   attempt IDs, cost rows, outcome mirrors. A bag of joins, not a record.

No domain survives. The "settlement" concept collapses in every case to a
derived view over native states plus time windows — useful analytics, not a
new canonical fact.
