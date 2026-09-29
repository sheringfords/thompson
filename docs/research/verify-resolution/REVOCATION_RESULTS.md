# Revocation Results (Phase 7)

Source: `revocation_test.go`. Core question answered yes: evidence is revoked
without pretending the computation never occurred.

- Revoked v1 claim: ArtifactRecord immutable and available; resolution moves
  to VERIFY; v2 re-verifies the SAME bytes (byte identity asserted); history
  (revoked claim, reason) stays inspectable but unusable (re-resolution
  refuses REUSE).
- Independent verdicts: v2 ACCEPTs the same bytes in the rotation case and
  REJECTs them in the tightened-threshold case — the verdict follows the new
  contract, never the old claim; rejection costs verification only (no
  production).
- Duplicate/stale revocation deliveries are no-ops (idempotent by state).
- Conflicting evidence (same ID, unexpected version): refuses REUSE,
  resolves VERIFY with the supersession reason (fail-closed, never majority).
