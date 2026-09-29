# Representation Analysis (Phase 10)

## One name per fact?

ComputationKey names the production fact (what bytes should exist);
VerificationKey names the claim fact (what is attested about exact bytes).
Rotation changes evidence (new claim rows), never computational identity
(same ComputationKey, same ArtifactRecord). Multiple claims reference one
record with zero artifact duplication (equivalence test: 1 record + 2 claims;
matrix rotations accumulate claims per artifact).

## Mutable coordination?

Claim rows are append-only (revocation appends a tombstone state, never edits
history). No fact lives in two mutable places: bytes in ArtifactRecord,
verdicts in VerificationClaims, identity in keys. The anti-pattern
(independently mutable duplicated truth) does not occur — the one shared
reference (artifact digest) is immutable content addressing, requiring no
reconciliation.

## Net effect

Splitting deletes the conservative coupling (recompute-on-rotation: 519 nodes
in the materialization assay) and replaces it with one claim row per rotation
(~200B + fsync). No new mutable coordination is introduced.
