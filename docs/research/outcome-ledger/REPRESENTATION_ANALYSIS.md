# Representation Analysis (Phase 10)

Question: is there a new canonical fact, or another representation of
existing records?

## Field audit of the candidate (authoritative / referenced / copied / derived)

- Job identity, intent, timestamps: REFERENCED (owned by incumbents).
- Attempt telemetry, model/release IDs: REFERENCED (owned by providers/runtimes).
- Cost events: REFERENCED (owned by consoles, invoices, time systems).
- Outcome observations: REFERENCED (owned by business systems).
- Settlement verdict: DERIVED (rule over native states + windows) — the only
  candidate for Thompson authorship. But it is a view, not a fact: recomputed
  identically by any warehouse query, with no exclusive inputs.
- Corrections: REFERENCED (native reopen/reversal/regression records).
- Economic rollups: DERIVED (arithmetic over referenced costs).

After eliminating copied mutable state (all of it — nothing may be copied
without an owner) and derived values (settlement, rollups), Thompson would
authoritatively own NOTHING: no Settlement (derivable), no attribution
(heuristic-bound), neither. Per the mission rule — if Thompson owns no
unique authoritative fact, the product thesis fails — it fails here.

## Desired shape, tested

"External systems own observations; Thompson owns only the cross-authority
settlement/history that cannot exist inside one source system." No such
history was found that is not native somewhere already (helpdesk reopen
trails, ledger reversals, CRM history, git timelines). The shape is empty.
