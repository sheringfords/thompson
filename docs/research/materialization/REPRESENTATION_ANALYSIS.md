# Representation Analysis (Phase 10)

## Are ExecutionKey + VerifiedArtifact + edges sufficient?

Yes for identity/validity/provenance: the materialization graph is DERIVED
from artifact records (key→deps projection); no parallel cache/provenance
structures exist in modes C/D; planning quotes read the same records (no
translation tables — the quoter reconstructs keys from the same constructors
as execution). Duplicated mutable facts introduced by the assay: per-runner
outcome tables and Bodies maps (test-harness stand-ins for journal replay
and the content store — scaffolding, clearly marked), B/B+ entry metadata
(contract/job/version — the evidence a conventional cache would need to
reimplement to match Bp≈C correctness, which is itself the collapse
argument), calibration means (quote-only).

## Code/state vs removed coordination

Added: ~1.6 kLOC (`corpus/ops/cache/modes/run/material/analyzer` + tests),
zero new dependencies, zero production files. Removed coordination: none
demonstrated — the honest result is that modes B/Bp already coordinate
minimally via content keys, so there is no recomputation machinery for C to
remove on this workload shape. Replacing B's cache table with C's
store+validity machinery WITHOUT deleted coordination elsewhere would be
exactly the anti-collapse the mission warns about: not done, reported.

## Workload-specific invalidation?

None: invalidation is generic store evaluation + content edges. The assay
rejects the architecture only if it needed per-op invalidators — it did not.
But genericity without incremental value still fails the decision gate.
