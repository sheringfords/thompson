# Invalidation Results (Phase 5)

Source: `TestTransitiveInvalidationChain`, `TestOneDepChangeStales`,
`TestVerifierChangeStales`, `TestRestoreDoesNotAutoRevive`, fan-out benchmarks,
and `testdata/reuse_{dev,heldout}_results.json`.

## One-dependency-at-a-time

Every frozen mutation produced the expected validity on both workloads, dev and
held-out seeds: single dep change → STALE with `changed_dep` naming the exact
dep; verifier-contract change → STALE; irrelevant metadata → VALID (no-op).
Unrelated artifacts remain VALID (chain test publishes an unrelated artifact and
re-evaluates it VALID after A-stale propagation).

## Transitive chains (A→B→C via `upstream:<key>` edges)

Leaf-dep change on A staled A, then B, then C, each recording reason + originating
dep (`transitive: upstream <digest16> stale (lockfile)`). Correction on A's job
INVALIDATED A, B, C. Restore semantics: exact original world + still-authoritative
source → VALID again; exact world + corrected source → stays INVALID (no auto-revive).

## Shared subgraphs / fan-out (measured)

- Chain L=500 correction propagation: 500 invalidated, 2.66 s total (~5.3 ms/edge).
- Star 1→300 stale fan-out: 301 staled, 5.33 s total (~17.7 ms/edge).
- Per-edge cost is fsync-per-state-record in this prototype (macOS ~6 ms/sync),
  NOT traversal: graph walk itself is map-based and negligible. Group-commit would
  collapse this ~1000x; deliberately NOT implemented (prototype honesty over speed).

## Comparators (dev corpus: W1 50 + W2 40 runs; held-out: W1 70 + W2 56 runs)

| Comparator | Necessary recomputations | Unnecessary recomputations | Incorrect retained |
|---|---|---|---|
| Rerun everything (B0) | all (50+40 dev) | all repeats of identical worlds (dev: 10 W1 + 10 W2 — repeat + irrelevant scenarios that a correct cache serves) | 0 |
| Primary-input-only (B1) | 5+5 dev (changed-input misses) | 0 | dev: 30 W1 + 25 W2; held-out: 42 W1 + 35 W2 false reuses |
| Dependency-derived (B2) | all fresh on STALE-miss/correction (dev 40+30, held-out 56+42) | 0 (zero incorrect invalidation both corpora) | 0 both corpora |

B1's incorrect-retained break down as: bytes-differ (toolchain/config/schema/executor/
prompt/command/lockfile changes), verifier-mismatch (contract change), revoked outcome
(correction) — each case named in the JSON `false_reuse_cases`.

## Metrics

Propagation time: above. Graph-storage overhead: ~1.6 KB/record (state transitions
append; see economic results). Unnecessary recomputations eliminated vs B0 at repeat
rates R≥2 (see `ECONOMIC_AND_SCALING_RESULTS.md`).
