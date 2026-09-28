# Workloads and Mutations (Phase 3)

Implementation: `go/assay/reuse/workload.go`. Synthetic data only; deterministic
`splitmix64` PRNG (no `math/rand` version dependence).

## W1 — Coding validation

Repeatable repository validation over controlled source trees + toolchain identities.
`GenRepo(seed, 12 files, 64B)`: 12 synthetic Go files across 4 packages.
Computation `W1Validate`: digest-bound PASS artifact over
(tree, config, lockfile, toolchain, command). Modeled cost $0.50/run.
Independent verifier `W1Verify`: recompute-and-compare.
Key deps: tree digest (primary input), config, lockfile, command, toolchain
(executor), env allow-list (`GOOS=...` only — never the whole environment),
verifier contract `w1-verifier/v2`, policy `policy-default`.
Non-semantic exclusions (never in key): hostname, run id, wall-clock, log bytes.

## W2 — Structured document extraction

Deterministic extraction from synthetic documents, fixed 5-field schema, independent
field-level verifier. `GenDocs(seed, 4)`: 4 docs x {title,date,amount,party,clause}.
Computation `W2Extract`: schema-bound projection folded with schema/executor/prompt
versions. Modeled cost $0.20/run. Verifier `W2Verify`: field-level recompute.
Key deps: document digest, schema version, prompt version, executor version,
verifier contract `w2-verifier/v1`, policy `policy-default`. `EnvDigest` empty:
the extractor declares no environment inputs (documented, not omitted silently).

## Frozen mutation sets (`Mutations()`)

W1 (10/scenario): repeat-identical, one-source-file, config, lockfile, toolchain,
command, env, irrelevant-metadata, verifier-contract-change, authoritative-correction.
W2 (8/scenario): repeat-identical, source-document, schema, executor, prompt,
irrelevant-metadata, verifier-contract-change, authoritative-correction.
Expectations: repeat/irrelevant → VALID; every single-dep mutation → STALE;
verifier change → STALE; correction → INVALID.
`BaseWorld` + seeds frozen before final benchmark: dev `11,22,33,44,55`,
held-out `1001..1007` (reserved; prototype never developed against them).

## Success criteria (met)

Reused W1 artifact matches fresh execution and `W1Verify` accepts it; reused W2
artifact matches fresh extraction and `W2Verify` accepts it — checked byte-for-byte
against fresh ground truth on every hit (`isFalseReuse`, `assay.go`).
