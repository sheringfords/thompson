# Resolution Contract V1 (Phase 2)

Deterministic resolver: `resolve(computationKey, requiredVerificationKey,
liveWorld, authority) → (REUSE | VERIFY | RECOMPUTE | UNKNOWN, reason)`.

## Semantics

- **REUSE**: exact ArtifactRecord exists AND an authoritative ACCEPTED
  VerificationClaim exists for the CURRENTLY required VerificationKey.
- **VERIFY**: exact ArtifactRecord exists for the required ComputationKey, but
  no current ACCEPTED claim does (absent, stale contract, superseded, revoked,
  or different verifier/version). Re-verification must prove byte identity
  first (integrity), then run the new verification.
- **RECOMPUTE**: no exact artifact for the ComputationKey, or a
  computation-affecting dependency changed.
- **UNKNOWN** (fail-closed): required identity, evidence, dependency, or
  authoritative status cannot be established. Never reusable, never a shortcut.

## Rules (tested)

Old ACCEPTED claims never satisfy a different VerificationKey (key includes
artifact + contract digests — cross-key satisfaction is a digest mismatch by
construction). Verifier revocation yields VERIFY when the artifact record is
intact (new claim required, bytes untouched). Corrupted bytes (integrity
failure) yield RECOMPUTE or UNKNOWN, never VERIFY. Computation-dependency
changes always resolve RECOMPUTE (classification boundary tested in Phase 9:
misclassified deps are detected via oracle mismatch, never patched per
workload).
