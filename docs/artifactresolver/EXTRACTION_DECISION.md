# Extraction Decision (Phase 11)

## Against EXTRACTED_PRIMITIVE_READY_FOR_DURABILITY_ASSAY

1. Semantic parity with PR #33: 6 frozen scenarios, exact (state, reason,
   artifact) equality, zero discrepancies; history shape matches (timestamps
   excluded by documented design difference).
2. Four-way resolution correct: 8-test corpus green (states, classification,
   collision, corruption, rotation-with-zero-production, revocation,
   conflicting evidence, cross-contract refusal, restart).
3. Zero false ACCEPTED: asserted in corpus and parity; none observed.
4. No dependency on rejected architecture: stdlib-only closure, verified by
   `go list` + import/concept grep (evidence in DEPENDENCY_REVIEW.md).
5. API limited to the concern: 4 functions + constructors/lookups/types;
   anti-goals (server/CLI/HTTP/scheduler/callbacks/DSL/policy) absent.
6. No duplicated mutable authority: representation audit clean.
7. Clean-main reproduction: full `go test ./...`, race, vet green on a
   checkout containing no research branches.

## Decision: EXTRACTED_PRIMITIVE_READY_FOR_DURABILITY_ASSAY

All seven hold. No KEEP/STOP condition triggered: no hidden DAG/replan
dependence surfaced, no workload logic required, no second authority
introduced, no false ACCEPTED appeared, no machinery grew.

## Smallest justified next engineering task

The journal-backed Store experiment proposed in DURABILITY_NEXT.md
(same four-function API, crash-fault equivalence). Proposed only.
