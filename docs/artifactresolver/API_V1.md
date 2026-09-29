# Artifact Resolver API V1 (Phase 3)

Public surface of `go/artifactresolver` (standard-library-only). Four
functions plus constructors, lookup helpers, and types. No workflow
execution, planning, routing, approval, or effect methods. No server, CLI,
HTTP, scheduler, callbacks, DSL, or policy.

## Functions

- `Resolve(ck ComputationKey, vk VerificationKey, liveComp, liveVerify map[string]string, authority AuthorityLookup) Decision`
  Deterministic REUSE | VERIFY | RECOMPUTE | UNKNOWN with a machine-readable
  `Reason` on every path. UNKNOWN fails closed. Storage is read, never
  written, by resolution.
- `(*Store).RecordArtifact(key ComputationKey, body []byte, receipt string, cost int64) (*ArtifactRecord, error)`
  Publishes immutable bytes. Idempotent for byte-identical republication;
  same-key/different-bytes refuses (`ErrConflict`). Storage behind the
  `Store` type only (single caller: this package's tests and future
  integrators — no speculative storage interface).
- `(*Store).RecordVerification(vk VerificationKey, status ClaimStatus, evidence string, authVersion uint64) (*VerificationClaim, error)`
  Appends a claim. History preserved; latest per key governs.
- `(*Store).RevokeVerification(keyDigest, supersededBy string) (bool, error)`
  Marks revoked without touching bytes or history. Idempotent no-ops for
  duplicates; unknown keys error.

## Read helpers (no side effects)

`Open`, `Close`, `LookupArtifact`, `LookupClaim`, `ClaimKeysForArtifact`
(audit aid), `Body`, `Stats`. `CorruptBody` exists solely for fault-injection
tests of corruption handling; production callers must never use it.

## Types

`ComputationKey`, `VerificationKey`, `Dep`, `ArtifactRecord`,
`VerificationClaim`, `ClaimStatus` (ACCEPTED/REJECTED/UNKNOWN), `Authority`,
`AuthorityLookup`, `Resolver`, `Resolution`, `Decision`, `Store`,
`ErrMissingMeta`, `ErrConflict`. Nothing else is public surface.
