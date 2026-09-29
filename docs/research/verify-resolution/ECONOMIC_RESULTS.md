# Economic Results (Phase 8)

Source: `TestEconomics` (`testdata/vr_economics.json`). Benefit reported only
as production work actually avoided (R0prod − R1prod). Overhead = resolver
(lookup/evaluate/integrity) + claim fsyncs + re-verification.

## Measured (darwin, real CPU)

- W2 burn=1/10/100 (4 rotations): avoided 5/156/956ms at ~200µs overhead —
  25× to 4700× coverage. Regime: above 10:1 everywhere measured.
- W1 (5 rotations): avoided 2ms at ~1ms overhead ≈ 2× coverage — marginal,
  reported as such (claim fsyncs dominate at this scale).
- Frequency sweep (burn=10): avoided scales linearly 0/15/44/82ms for 0/1/2/4
  rotations; overhead ~50µs/rotation. Zero rotations → zero benefit (honest
  baseline: the machinery only pays under rotation).

## Break-even (measured crossover, no fitted model)

- Burn≈1 (production ≈ verification scale): VERIFY saves little after
  overhead — regime below economic interest.
- Burn≥10 with ≥3 rotations: avoided production exceeds overhead by >5×.
- W1 near-parity production/verification: ~2× coverage — below the frozen
  5× bar; the primitive is not economical there.
- Unmeasured claim: production cheaper than verification (inverted ratio) —
  VERIFY can never win; not tested because no workload exhibits it honestly.
