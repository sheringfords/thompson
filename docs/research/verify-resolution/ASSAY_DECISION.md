# Assay Decision (Phase 13)

Rules frozen in WORKLOADS.md before evaluation (held-out set, 10:1 ratio,
5× coverage); applied without moving thresholds.

## Against CONTINUE_TO_ARTIFACT_RESOLVER_PRIMITIVE

1. R1==R0 verdicts, all resolvable rows, dev (12) + held-out (3): PASS.
2. Zero false ACCEPTED (R2 negative control caught wherever contracts
   reject): PASS.
3. Verification-only rotations → VERIFY with zero production (9 rows,
   R1ProdExec==0 asserted): PASS.
4. Computation-affecting changes → RECOMPUTE (comp-dep, corruption,
   prod-config, plus honesty checks): PASS.
5. Avoided production vs overhead in the ≥10:1, ≥3-rotation regime: W2
   25×–4700× coverage (PASS decisively); W1 ≈2× (below the 5× bar —
   recorded as the boundary case, not hidden).
6. Generic across W1+W2, zero workload-specific resolver logic: PASS.

## Decision: CONTINUE_TO_ARTIFACT_RESOLVER_PRIMITIVE

All six hold in the pre-registered beneficial regime (production-dominated,
rotation-heavy verification). W1's marginality and the misclassified-dep
blindness (boundary scenario 1: undetected without re-deriving verifiers)
are retained as explicit preconditions, not stop signals: no false ACCEPTED
occurred, and honest classification is the documented admission criterion.

## Negative findings retained

- W1-scale economics (~2×) do not justify the primitive alone.
- Claim-write fsyncs dominate resolver overhead at small scale.
- Generation-side misclassification is invisible to the resolver.
- R2 (stale-claim reuse) fails exactly where it should — kept as control.

## Smallest justified next engineering task

Harden the 4-function resolver API (`resolve`/`record_artifact`/
`record_verification`/`revoke_verification`) with batched claim durability,
still research-gated, reusing the frozen contracts unchanged. Proposed only.
