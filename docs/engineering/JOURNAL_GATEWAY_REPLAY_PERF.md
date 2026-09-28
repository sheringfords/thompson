# Journal Gateway Replay Perf (Phase 4)

## Bottleneck (measured, Darwin, matched 2-arm histories)
Pre-optimization, every adapter read was a full `EventsSince(0)` scan +
parse. A 10k-job journal paid it 4-5x per boot and 4x per settle:

| op (10k jobs / 20k rows) | before | after |
|---|---|---|
| quality resume (cold) | 713 ms | 36 ms post-load (310 ms one-time load) |
| cost rebuild (cold) | 429 ms | 44 ms post-load (same shared load) |
| cold restart full load (1 scan, all 3 kinds) | — (paid per call) | 310 ms, once per boot |
| decision scan 10k (warm) | full scan | 0.8 ms |
| safety events (warm) | full scan | 0.1 ms |
| per-settle Lookup / Latest / history | full scan each (~1 s/settle at 10k) | map hit / cache (µs) |

1k-job journal recovery is now at or below JSONL (2-4 ms vs 2-11 ms);
10k-job boot totals ~390 ms vs JSONL ~30 ms.

## Fix (backend-internal, no semantic change)
`go/gateway/journalstore/store.go`: the `Backend` owns one replay
projection (decisions + by-ID index, outcomes + latest-version index,
safety, exec markers). Reads tail-sync (`EventsSince(maxSeq)`, usually
zero rows) and serve from memory; writes commit to the journal first,
then sync the tail. Stores are singletons sharing the projection
(router previously built two decision/outcome instances — same rows,
now one cache). Ordering stays commit (seq) order; duplicates/stale
rules still enforced by the journal; `MarkExecution` is memory-only
exactly as before (old comment claimed a journal row family that the
code never wrote — corrected).

## Not pursued (out of pilot scope)
Single-boot load is one SQLite scan + JSON parse (~30 ms / 2k rows,
linear). Cutting it further needs assay-format changes (binary
payloads, covering indexes) or checkpointed partial replay — recovery
semantics or shared-schema changes the pilot must not make. The
remaining per-settle refold cost (learner/book rebuild over history)
is the same algorithm on both backends (JSONL included), not a
journal gap.
