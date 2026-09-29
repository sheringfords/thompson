# Durability Next Steps (Phase 8, design only)

No batched durability implemented in this mission (per-request fsync retained
from the proven assay design). State requiring durability: artifact records
(bytes + identity), claim appends, revocation appends.

## Batching analysis

- Safe to batch: consecutive claim appends for distinct keys (no reader can
  observe a partial batch as inconsistent — resolution reads latest per key,
  and keys are independent).
- Requires care: revocation appends (a crash between the decision to revoke
  and the revoke record must not leave REUSE possible — mitigation: resolve
  against the in-memory mark first, fsync second, already the current order).
- Atomicity: claim-append vs revocation-append for the SAME key need not be
  atomic (last-writer-wins per key with full history retained makes
  interleavings explainable); artifact-append vs first-claim-append need no
  transaction (artifact without claim resolves VERIFY, never REUSE).
- Crash points: between every append (current fsync makes each a prefix-
  consistent recovery point — same posture as the journal assay).

## SQLite journal reuse (assessed, not implemented)

The merged journal backend (PR #28) could host these three record kinds as
event payloads without coupling the resolver to gateway/runtime semantics
IFF the resolver keeps its own key/digest vocabulary and treats the journal
as an opaque ordered byte log (no safety/monitor/policy tables). Proposed
next experiment: journal-backed Store implementation behind the same
four-function API with crash-fault equivalence tests. Not implemented here.
