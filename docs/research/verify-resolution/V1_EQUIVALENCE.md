# V1 Equivalence (Phase 3)

V1 records map losslessly into the split model (mapping test-only, no
migration path): V1 executor/env/policy/operation/inputs/deps → ComputationKey;
V1 verifier-contract → VerificationKey contract; V1 outcome → claim evidence.
Proven: verifier rotation (same computation, v1→v2, deterministic identical
bytes) collapses to ONE ArtifactRecord + TWO per-contract claims; cross-key
claim satisfaction is a digest mismatch by construction.

Hard gate holds: mapping two byte-distinct V1 records onto one ComputationKey
is REFUSED (conflict error, tested) — the split never makes distinct
computations identical. (Real V1 stores cannot produce this input: V1's own
same-key conflict rule already prevents it — compatible by construction.)

Decision reproduction: V1-VALID+current-claim → REUSE; V1-STALE by
computation dep → RECOMPUTE; V1-STALE by verifier → VERIFY; V1-INVALID
(revoked source, intact bytes) → VERIFY; V1-UNKNOWN → UNKNOWN. All five
reproduced from the split representation; V1 behavior is preserved and
refined (STALE disambiguated by cause), never contradicted.
