# Plan Selection Results (Phase 7)

Source: D rows of `mat_economics.json` + quoter behavior. Predeclared plans:
staged (16 shards → combine → report → attest) vs direct (fold+format →
attest). Declared weights: shardagg 8, combine 8, report 2, direct 30,
attest 4, verify 1; lowest wins, ties → staged.

## C vs D (incremental value after materialization)

D chose direct on every N=1000 scenario (cold and warm): direct-remaining
(30+4) beats staged-remaining whenever any shard misses, because the global
combine re-executes on any change in both shape... — i.e. staged reuse of
combine/report almost never materializes. Measured D-vs-C deltas are small
and noise-dominated (e.g. chg-1pc D 375ms vs C 360ms; enrichment D 4203ms
vs C 4666ms). D provides no systematic incremental work value here: the
choice is static (direct dominates) rather than adaptive. Cases where D
chooses staged and wins: none observed (full-reuse versions tie at 0, broken
to staged — zero value by construction).

## Verdict on RQ4

Plan selection is correctly implemented (deterministic, quoted, recorded per
row) but adds no measurable value beyond C on this workload: the ranking
does not flip with state often enough to matter. The staged shape's global
combine is the structural reason — a finer-partitioned combine (per-shard
reports) might create flips, but that redesigns the workload to favor the
optimizer and is explicitly not done.
