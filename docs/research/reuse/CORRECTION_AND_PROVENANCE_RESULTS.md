# Correction and Provenance Results (Phase 6)

Source: `TestCorrectionPropagation`, `TestConflictingEvidenceFailsClosed`,
`TestCrashRestartReconstruction`, `TestRestoreDoesNotAutoRevive`, comparison runs.

## Correction flow (tested)

ACCEPTED execution → published artifact (VALID) → authoritative correction
supersedes outcome v1/ACCEPTED → v2/REJECTED → `PropagateCorrection` marks the
artifact INVALID + transitively INVALIDATES downstream dependents (chain test:
2 records for 1 correction over a 2-graph; L=500 chain: all 500).

## Delivery semantics

- Duplicate correction delivery (same version): no-op, 0 newly invalidated.
- Stale correction delivery (older version): no-op, 0 newly invalidated.
- Conflicting evidence (lookup reports v7/ACCEPTED vs bound v1): INVALID, never
  majority-vote, never VALID. Fail-closed confirmed by test.

## Replay determinism

`TestCrashRestartReconstruction`: publish → stale-mark → close → reopen from file
alone → record, state, reason and bytes all reconstructed; exact replay evaluates
VALID again. The store replays `publish`/`state` records in file order; fsync per
commit gives prefix-consistent crash recovery (same posture as the journal assay).

## Provenance after invalidation

Invalidation never rewrites history: `Verification` stays ACCEPTED, `EvidenceID`,
`ReceiptID`, `OutcomeJobID/Version`, dep snapshot and `Reason`/`ChangedDep` remain
readable on INVALID/STALE records. A STALE artifact is historically intact but not
reusable — the outcome taxonomy (ACCEPTED) and reuse validity (STALE/INVALID) stay
separate as the contract requires.

## End-to-end correction case (comparison matrix)

`authoritative-correction` scenario, all seeds, both workloads: B2 key hit →
Evaluate → INVALID → correct non-reuse, zero B2 false reuse. B1 same scenario:
hit on input-only key → reuses revoked bytes → FALSE REUSE
(`no ACCEPTED authoritative outcome`), every seed. This is the evidence-binding
dividend: the naive cache cannot see revocation; the artifact store can.
