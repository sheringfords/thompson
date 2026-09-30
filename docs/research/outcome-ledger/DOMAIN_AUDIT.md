# Domain Audit (Phase 2)

Method: incumbent data-model analysis from long-stable product semantics plus
two verified live documentation points (Anthropic platform usage/rate-limit
console model; GitHub Issues state/state_reason/timeline API; Stripe refund
object with reason/status/charge linkage). Where a product detail was not
directly re-verified, uncertainty is resolved AGAINST Thompson (incumbents
assumed capable) per the kill-test bias. No customer data; no agent runs.

## SUPPORT — resolve a support case (refund/credit/change/escalation)

- Systems: helpdesk SOR (Zendesk-class: ticket lifecycle, CSAT, reopen),
  payment system (refunds/credits/disputes), CRM (churn), agent/model provider.
- job_identity: ticket ID (stable, native).
- initial_success_signal: agent reply sent, ticket marked solved.
- authoritative_success: CSAT good + no reopen within window + no linked
  refund/credit/dispute. Days-to-weeks delayed.
- correction_path: reopen (native), CSAT revision, refund issued later.
- cost_sources: provider console (tokens), agent platform per-resolution
  fees, human touch time (helpdesk time-tracking/activity).
- loss_sources: refunds/credits/chargebacks (payment system, native reason/
  status fields), churn (CRM).
- authorities: helpdesk owns case state; payments own money movement; CRM
  owns account outcome; provider owns tokens. Genuinely split — but all
  mutually joinable by ticket/customer IDs in a warehouse.
- missing_join: none structural. "Which attempt resolved the case at what
  fully loaded cost" = ticket events (author-tagged) + CSAT + payments +
  usage exports. Ordinary joins.
- incumbent_solution: custom ticket fields + event history already hold
  resolution provenance; the helpdesk could add agent-release tags natively.
- vendor_capture_risk: LOW — CSAT/refunds/churn never pass through the
  provider; it cannot calculate case economics without business systems.

## ITSM — resolve a service request/incident

- Systems: ITSM suite (ServiceNow-class) + provider.
- job_identity: incident/request number (stable, native).
- initial_success_signal: agent-applied fix / workaround, ticket resolved.
- authoritative_success: resolved + SLA met + no recurrence/reopen.
- correction_path: reopen (native), problem record linkage (native).
- cost_sources: provider tokens; resolver labor via native time-tracking.
- loss_sources: downtime cost (modeled everywhere — no system observes it,
  Thompson included).
- authorities: essentially ONE system (state, SLA, labor, reopen, changes).
- missing_join: none. The ITSM suite already is the episode record.
- incumbent_solution: already exists.
- vendor_capture_risk: LOW (same reasoning as SUPPORT).

## AP — process/approve/reconcile an invoice

- Systems: ERP/accounting SOR, AP workflow, bank/payment rail, provider.
- job_identity: invoice ID (stable, native).
- initial_success_signal: extracted, matched, approved.
- authoritative_success: posted + paid + 3-way matched; period close clean.
- correction_path: void/reversal/credit memo — accounting's NATIVE
  settlement semantics with full audit trail. Corrections are first-class
  here, not a gap.
- cost_sources: provider tokens, approver labor (workflow timestamps),
  bank/payment fees (native).
- loss_sources: duplicate payment, fraud, late fees — all inside ERP/bank
  records.
- authorities: the ERP owns postings; the bank owns money. Both definitive.
- missing_join: none. Accounting systems ARE settlement systems; the agent's
  extraction work lives inside the approval the ERP already governs.
- incumbent_solution: exists by definition (the ledger is the product).
- vendor_capture_risk: LOW.

## SALES_OPS — qualify/enrich/route/advance a lead

- Systems: CRM (Salesforce-class: stages, history tracking, activities),
  enrichment vendors, provider, outreach/sequencing tools.
- job_identity: lead/opportunity ID (stable, native).
- initial_success_signal: scored, enriched, routed, stage advanced.
- authoritative_success: closed-won, months later; stage history is the
  correction trail (native field-history tracking).
- correction_path: stage regression, disqualification reversal (native).
- cost_sources: provider tokens, enrichment fees (vendor invoices), SDR labor
  (activities, partially).
- loss_sources: killed good leads — COUNTERFACTUAL, unobservable in every
  system including any Thompson (no ground truth exists). Misrouted effort —
  visible in activity/stage data.
- authorities: CRM owns stages/history; vendors own fee events; provider owns
  tokens. Joinable by lead ID.
- missing_join: none for knowable facts. The unknowable part (counterfactual
  loss) is not a record-shaped gap.
- incumbent_solution: custom objects + history + warehouse cover everything
  knowable; the CRM could natively tag agent attempts.
- vendor_capture_risk: LOW for economics (no visibility into stages/won).

## SOFTWARE_ENGINEERING — bounded task to code/CI/review/merge-or-fail

- Systems: git host (code truth), CI (build truth), issue tracker (intent),
  incident system (production truth), provider (cost). Verified live:
  GitHub Issues carry state, state_reason (completed/reopened/not_planned),
  timelines, linked PRs, checks, review events.
- job_identity: issue/PR number (stable, native).
- initial_success_signal: CI green + merged + issue closed.
- authoritative_success: merged + checks green + issue closed-as-completed +
  no revert + no attributed incident within window. Delayed days–weeks.
- correction_path: revert PR, issue reopen (both native), follow-up fix.
- cost_sources: provider tokens (console), CI minutes (metered natively),
  reviewer time (review events exist; effort→cost is modeled).
- loss_sources: incident cost (modeled; heuristic deploy→incident linkage),
  escaped-defect rework (follow-up issues, observable).
- authorities: genuinely split across four systems with no shared schema —
  the closest call in this audit.
- missing_join: production-outcome attribution (deploy→incident→PR) is
  heuristic everywhere. BUT: Thompson faces the identical heuristic — it
  cannot observe causation either, and per attribution discipline must not
  over-attribute. The warehouse can do everything Thompson could here
  (PR/checks/timeline/issues API + CI minutes + usage + incident tags).
- incumbent_solution: issue state_reason + timelines + checks + linked PRs
  already encode the correction-aware lifecycle; a warehouse view
  (merged ∧ green ∧ closed-completed ∧ no-revert ∧ no-reopen) implements the
  settlement rule with no new semantics.
- vendor_capture_risk: LOW (provider never sees merge/revert/incident truth).

## Cross-domain pattern

Every domain has: stable job IDs, native outcome states, native correction
paths, metered cost sources, and join keys. The hard parts (downtime cost,
counterfactual lead loss, incident causation) are unobservable or heuristic
for ANY system — not Thompson-shaped gaps. No domain exhibits a fact that is
both (a) authoritative and (b) ownable only by a new cross-system record.
