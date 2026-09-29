# Workloads (Phase 4)

Implementation: `go/assay/verifyresolution/workloads.go` (+ `treatment.go`
R0/R1/R2). Same generic resolver (`resolve.go`, zero workload branches)
serves both. Synthetic data only; real deterministic CPU; no sleeps.

## W1 — versioned structured document artifact

Production: parse/extract/normalize 24 docs into one structured artifact
(hundreds of µs). Verification: schema/business-rule contracts, re-derived
checks over artifact + sources (µs). Frozen rotations: v1 baseline,
v2-strict (cap 10000), v3-relaxed (loose), v4-newfield (tags required),
v5-noinvariant (cap dropped), v6-bugfix (title non-empty), each flipping at
least one verdict cell in the matrix (verified by the R2 negative control).

## W2 — generated computational artifact

Production: iterated SHA-256 transform, burn factor 1–100 (5ms–1s).
Verification: threshold/invariant checks over bytes (tens of µs). Frozen
rotations: v1 baseline, v2-new-invariant, v3-threshold (t1 rejects burn>10),
v4-additional (parity), v5-rotation. Production/verification ratios measured
100:1 to 27000:1.

## Frozen economic regime + held-out set (Phase 13 pre-registration)

- Minimum economically meaningful production/verification ratio: **10:1**.
- "Materially exceeds overhead": avoided production ≥5× (resolver + claim
  fsync + verification) in the ≥10:1 regime with ≥3 rotations per artifact.
- Held-out (never developed against): seeds 9001–9003; rotations
  `w1-schema/v7-relaxed2`, `w2-policy/v6-tightened`; asserted in
  `TestHeldOutMatrix` (oracle verdict equality + zero false ACCEPTED).

## Oracle and controls

R0 (fresh produce + verify) is the oracle. R1 resolves (REUSE: no work;
VERIFY: integrity + verify; RECOMPUTE: produce + verify; UNKNOWN: fail).
R2 reuses stale claims (negative control — must be caught by oracle mismatch
wherever the new contract rejects).
