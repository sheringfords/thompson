# Artifact Resolver Contract V1 (Phase 1)

Transcribed from the PR #33 split-identity model with assay terminology
removed. Semantics preserved exactly; API surface narrowed to four functions.

## Public types

- **ComputationKey**: exact identity of production semantics — operation,
  operation version, canonical primary inputs, computation-affecting
  dependency digests, executor/env/generation identity where output semantics
  depend on them. Answers what bytes should exist. Never carries verifier
  identity unless the verifier also generates bytes.
- **ArtifactRecord**: immutable content-addressed produced artifact —
  ComputationKey digest, artifact digest, producing-execution identity,
  provenance metadata, actual production cost. Bytes immutable once published.
- **VerificationKey**: exact identity of a verification claim over a specific
  artifact digest — artifact digest, contract digest, verifier
  identity/version, verification-affecting dependency digests.
- **VerificationClaim**: versioned authoritative ACCEPTED | REJECTED | UNKNOWN
  evidence — key digest, artifact digest, evidence identity, verified-at,
  authority version, revocation/supersession state. Append-only history.

## Resolution states

- **REUSE**: artifact exists and the current exact verification claim is
  authoritative ACCEPTED.
- **VERIFY**: artifact exists and remains exact, but the required claim is
  missing, stale, superseded, revoked, or from a different contract/version.
- **RECOMPUTE**: required computation identity changed, or trusted artifact
  bytes do not exist.
- **UNKNOWN**: required identity, integrity, or authoritative evidence cannot
  be established. Fail-closed, never reusable.

## Fail-closed rules (retained)

Exact-digest identity only; same-key/different-bytes publication refuses;
history append-only; revocation idempotent and never mutates bytes; old
ACCEPTED claims never satisfy a different VerificationKey; corrupted bytes
yield RECOMPUTE or UNKNOWN, never VERIFY; computation-dependency changes
never downgrade to VERIFY.
