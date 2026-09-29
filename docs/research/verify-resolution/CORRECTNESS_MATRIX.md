# Correctness Matrix (Phase 6)

Source: `TestResolutionMatrix` (`testdata/vr_matrix.json`), 12 frozen
scenarios + 3 held-out. Hard gates: R1 verdict == R0 verdict in every
resolvable row; zero R1 false ACCEPTED; no cross-key claim reuse (structural:
keys bind artifact+contract digests); computation changes → RECOMPUTE;
verification-only changes regenerate no bytes (R1ProdNS == 0 on all 9
VERIFY rows).

## Notable cells

- strict-reject / bugfix-flip / compdep-reject-variants: R0 REJECTED,
  R1 REJECTED via VERIFY/RECOMPUTE, R2 stale-ACCEPTED (caught) — the negative
  control proves the gate has teeth.
- relaxed-accept: prior REJECTED claim + relaxed contract → VERIFY → ACCEPTED
  (revocation-free path to acceptance without reproduction).
- corrupt-bytes: RECOMPUTE (never VERIFY against untrusted bytes).
- unresolvable: UNKNOWN, no bytes, no verdict (fail-closed).
- Held-out (9001–9003, unseen rotations): oracle-equal, zero false ACCEPTED,
  all VERIFY (including the tightened-threshold REJECTED cell).
