# Representation Audit (Phase 7)

Core invariant: one name per fact; no mutable reconciliation between
artifact truth and verification truth.

## Findings

- Production identity: exactly one representation (`ComputationKey` →
  `CompDigest`). No node IDs, no plan-local aliases, no translation table.
- Content identity: exactly one representation (SHA-256 `ArtifactDigest` over
  bytes; bytes stored once, referenced by digest everywhere).
- Claim identity: exactly one representation (`VerificationKey` digest over
  artifact digest + contract + verifier + verify deps).
- Claims reference artifacts ONLY through immutable digest identity
  (`VerificationClaim.ArtifactDigest`; lookup by digest; no pointers, no
  mutable joins).
- Duplicated mutable fields between ArtifactRecord and VerificationClaim:
  none. `ArtifactDigest` appears in both but is immutable content addressing,
  not mutable truth. Timestamps (`ProducedAt`, `VerifiedAt`) are
  write-once record metadata, never read by resolution. `AuthVersion` lives
  only on the claim; revocation state only on the claim.
- Revocation affects claims only: the revoke path writes a claim-state line;
  artifact records and bytes are never rewritten by revocation, corruption
  handling, or re-verification (asserted by tests).

## Verdict

No duplicated mutable authority. No reconciliation logic exists because
there is nothing to reconcile. The audit finds the representation minimal
for the retained semantics.
