# Exact Cache vs Materialization (Phase 5)

Source: `TestEconomicsN1000` (`testdata/mat_economics.json`), N=1000.

## Primary outputs (charter scenarios, total measured ns)

| Scenario | B | C | C vs B |
|---|---|---|---|
| chg-1pc | 264ms | 423ms | −60% (C loses) |
| chg-5pc | 868ms | 1037ms | −20% |
| schema-change | 15.8s | 16.5s | −5% |
| enrichment-change | 4.1s | 4.3s | −6% |

Charter gate: 0/4 pass (needed 2). Exec closures IDENTICAL in every cell
(B==Bp==C==D executed ops): content-addressed per-op caching already finds
the minimal closure for pure functional graphs. The entire delta is
machinery: C ≈ 25µs/node (key canonicalization + Evaluate + VerifyBytes)
vs B ≈ 2µs/node (lookup + integrity re-hash).

## Decomposition (B → Bp → C)

- B→Bp (verification-memory): ±noise (µs). Evidence memory is nearly free;
  Bp additionally refuses revoked evidence and re-verifies on rotation.
- Bp→C (graph): NEGATIVE six figures (µs) on every sparse scenario — the
  graph traversal/validity machinery costs real work and eliminates zero
  additional operations beyond what flat exact keys already skip.
- Net: C's incremental work value over a credible exact cache is negative
  on this workload; C's incremental CORRECTNESS value is one revoked-evidence
  refusal and contract-currency per reuse (B: 519 stale + 1 revoked).

## Overhead accounting (all metered, in-test)

C overhead = hashing/key-building + store Lookup/Evaluate + VerifyBytes +
publish fsyncs + STALE/INVALID transition writes. B overhead = key hashing +
lookup + integrity re-hash + terminal semantic verify + cache fsyncs.
Net benefit of C vs B after overhead: negative on all 10 mutations except
full-reuse versions measured against B-without-memory (no-change: C 114ms
vs B 7ms — C still loses; the win exists only vs a hypothetical B that
re-verifies semantics per hit, which no credible cache does).
