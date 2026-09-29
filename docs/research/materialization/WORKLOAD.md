# Materialization Workload (Phase 2)

Implementation: `go/assay/materialization/` (`corpus.go`, `ops.go`,
`cache.go`, `modes.go`, `run.go`, `material.go`). Generated synthetic records
only (seeded PRNG; no customer data, nothing external). Real deterministic CPU
over actual bytes (repeated SHA-256, field parsing, validation scans);
no sleeps; no modeled costs (work accounting uses measured ns throughout).

## Shape

Per record (record-local): `validate → normalize → extract → verify`, plus
`enrich` joining the shared taxonomy. Cross-record: 16-shard aggregation
(`shard-%02d`, global `topk` param in shard keys) → `combine` → `report` →
`attest` terminal (full revalidation scan over the report + verify rollup).
Node counts: 5N + 20. Direct alternative path: `direct` (fold + format in one
expensive op over all enriched) → `attest`.

Shared intermediates: taxonomy (all enrich), per-shard aggregates, combine.
Record-local: validate/normalize/extract/verify/enrich. Global aggregate
dependency: `topk` param in shard + report keys. Sparse changes: record
fractions mutate leading records only.

## Mutation series (frozen, applied independently to V0)

`no-change`, `chg-1pc`, `chg-5pc`, `chg-20pc`, `schema-change` (v3→v4, alters
extraction output), `verifier-change` (contract rotation, runner-level),
`enrichment-change` (tax v1→v2), `aggregate-change` (topk/10→25),
`revocation` (base verify-job revoked, runner-level), `full-replace`.
Fresh full recomputation (mode A) is the independent oracle (bytes + verdict).

## Correctness rules (frozen)

B keys: `(op, immediate input bytes, config)` — credible conventional cache;
documented gaps: no verifier contract, executor, outcome lineage or graph.
B verifies hits by integrity (output re-hash) + terminal semantic
verification per version. B+ adds evidence memory (contract + outcome checks;
re-verify on contract rotation; revoked → recompute). C/D use V1
EvidenceKeys + store validity + republication; contract rotation
conservatively re-executes (reported, not optimized). D selects staged vs
direct with DECLARED weights (deterministic): shardagg 8, combine 8,
report 2, direct 30, attest 4, verify 1; lowest total wins, ties → staged.
Expected ranking: cold → direct (34 vs 142); sparse warm → staged;
heavy change → direct; full reuse → tie (staged, zero value).
