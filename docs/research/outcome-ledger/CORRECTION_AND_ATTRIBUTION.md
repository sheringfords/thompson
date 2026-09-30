# Correction and Attribution (Phase 9, analytical)

Stress answers from audit evidence (no fixtures executed):

- Duplicate/out-of-order/late observations: incumbent event logs + warehouse
  dedup/windowing handle these today (ticket histories, ledger sequencing).
- Late outcome after UNKNOWN: native transition (ticket solved later,
  invoice paid later, issue closed later). No new state machine required.
- Correction of settled outcomes: native in all five (reopen, reversal,
  regression, revert). History preserved by the owning systems.
- Conflicting authorities: rare in practice here because each fact has ONE
  natural owner (money→payments/ERP, case→helpdesk, code→git). The conflict
  problem the candidate ledger was designed for does not materialize.
- Missing cost sources / partial human input: gaps are labor-observability
  gaps, identical for any proposer.
- Multi-release / agent-then-human jobs: attributable by author/actor tags
  already present in event streams.
- Outside causation: the binding constraint. Neither warehouse nor Thompson
  may over-attribute downstream outcomes to participating agents. Uncertainty
  must stay explicit — and explicit uncertainty needs no new system.

Nothing here requires Thompson machinery; everything here constrains any
claimant equally, including Thompson.
