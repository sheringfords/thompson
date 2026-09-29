# Correctness Results (Phase 4)

Source: `TestCorrectnessMatrix` (N=100, 10 mutations × 5 modes,
`testdata/mat_correctness.json`), economics gates (N=1000), scaling gates.

## Hard gates (all PASS)

- Zero incorrect C terminals (byte-equal to A on all 10 mutations, N=100/1000).
- Zero incorrect D terminals (vs same-shape oracle: staged→A, direct→direct oracle).
- Zero C/D false reuse (evidence-grade: contract currency + outcome lineage on
  every recorded reuse).
- Every C/D recomputation outside the necessary closure: none observed —
  exec counts match the minimal closure exactly (adversarial exact-count
  tests: fanout 120+19, global 480+19 at N=120).

## Findings per mutation (N=1000)

- Fractions/schema/enrichment/aggregate/full-replace: all modes byte-equal,
  verdicts equal; B/Bp/C/D exec counts IDENTICAL (minimal closure for all).
- Verifier rotation: B 5019 evidence-false reuses; Bp 0 (re-verifies); C/D 0
  with full conservative re-execution (519/5019 exec).
- Revocation: B 1 evidence-false reuse (bytes still equal); Bp/C/D recover
  with exactly 1 recompute (evidence rotation minimality); terminals equal.

## Undeclared-dependency fixture

A key built without the taxonomy dep reuses byte-stale results across a
taxonomy rotation. The assay DETECTS it (reused ≠ fresh bytes) but cannot
fail closed — declared-dependency identity cannot see undeclared inputs.
Documented limitation, not hidden; applies equally to B.
