# Split Identity Model V1 (Phase 1, candidate — V1 unchanged)

V1 (`ExecutionKey` + `VerifiedArtifact`) is NOT mutated by this assay.
Candidate assay-only types live beside it in `go/assay/verifyresolution/`.

## Candidate types

**ComputationKey**: operation identity/version, canonical primary inputs,
computation-affecting dependency digests, executor/env/generation identity
*where output semantics depend on them*. Answers: what bytes should exist.

**ArtifactRecord**: ComputationKey digest, artifact digest, bytes reference,
producing execution identity, provenance metadata, actual production cost.
Bytes immutable and content-addressed.

**VerificationKey**: artifact digest + verification-contract digest + verifier
identity/version + verification-affecting dependency digests. Answers: what
claim is tested about these exact bytes.

**VerificationClaim**: VerificationKey digest, artifact digest,
ACCEPTED/REJECTED/UNKNOWN, evidence identity, verified_at, authoritative
status, revocation/supersession info.

## Invariants (enforced in code, tested)

ComputationKey never carries verifier identity unless the verifier also
generates bytes; a new contract never inherits ACCEPTED; revoking a claim
never mutates its ArtifactRecord; UNKNOWN is never reusable; computation
changes → RECOMPUTE; verification-only changes → VERIFY candidate; no
semantic-equivalence heuristics in either identity.
