# DAG Workloads (Phase 3)

Implementation: `go/assay/plan/workloads.go` (+ `dedup_test.go` fan plan).
Same generic executor for both. Synthetic inputs + deterministic synthetic CPU
work (`burnCPU`: SHA-256 compressions, `WorkUnitHashes=2000`/unit — explicitly
synthetic systems work, never sleeps, never token claims). Every op has an
independent recompute verifier. Frozen seeds: dev `11,22,33…`, held-out
`1001,1002,1003`. Equivalence rule (predeclared): byte-equality against
full recomputation OF THE SAME physical plan + terminal ACCEPT; cross-plan byte
equality is NOT expected (different computations realizing one LogicalJob).

## D1 — structured-document pipeline

`validate(3) → extract(5) → normalize(4) → aggregate(6) → report(2) → attest(1,
terminal)` plus `summary(2)` sharing `normalize` (second output).
Alternatives: `d1-staged` (above, 23u cold) vs `d1-direct`
(`validate → direct(16) → attest`, 20u cold). Required properties: shared
intermediate with two outputs ✓; branch-only mutation (summary op-version,
aggregate op-version) ✓; verifier-contract change (summary/report) ✓;
authoritative correction (extract outcome revoked) ✓; irrelevant metadata
(job-id-only change → full reuse) ✓.

## D2 — coding-validation pipeline

`identity(2) → compile(8) → {unit-setup(3) → unit(5), lint-setup(3) → lint(5)}
→ aggregate(2) → attest(1, terminal)`. `unit-setup`/`lint-setup` are DISTINCT
node ids with IDENTICAL op+inputs (same ExecutionKey by construction):
the predeclared duplicate-key dedup case. Shared source+toolchain across
branches ✓; lintcfg change affects only {lint, aggregate, attest} ✓; toolchain
change invalidates compile closure, reuses identity ✓; repeated final job
reuses all valid nodes ✓. Alternatives: `d2-staged` (29 exec units, 37 with verification) vs `d2-direct`
(`identity → direct-check(19) → attest`, 22 exec units, 25 with verification).

## Workload-generic executor

No workload-specific invalidation, planning or scheduling code exists: `Run`,
`Quote`, `ChoosePlan` operate on `PhysicalPlan` + `Registry` only. Workload
differences live entirely in plan construction (`buildD1`/`buildD2`/`fanPlan`).
