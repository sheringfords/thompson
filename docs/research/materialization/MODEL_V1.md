# Materialization Model V1 (Phase 1)

ExecutionKey and VerifiedArtifact V1 are reused UNCHANGED. No Runtime/Run/
Proof semantics. No second node identity: graph nodes ARE ExecutionKey
digests; edges are the dependency lists already inside artifact records
(derived, never reconciled).

## Definitions

**MaterializedComputation** = exact ExecutionKey + VerifiedArtifact +
explicit dependency edges (the artifact's stored dep snapshot, including
`upstream:<key>` content edges). **Invalidation** is a property of dependency
validity (current digests + authoritative outcome state), never of execution
history. The **materialization graph** is the key→deps projection of stored
records. Artifact bytes live in the content store, bound by `artifact_digest`
(`VerifyBytes` on every reuse).

## States and invariants

States: VALID, STALE, INVALID, UNKNOWN, MISS (MISS = no record; UNKNOWN =
fail-closed, never reusable). Invariants: VALID needs exact key identity plus
authoritative ACCEPTED verification; any declared-dep change invalidates
exactly the dependent closure (content edges propagate); unrelated records
stay reusable; historical ACCEPTED never implies currently VALID; corrections
propagate transitively; no semantic similarity anywhere; every C/D
recomputation outside the necessary closure is itemized as overhead or
conservative invalidation (never silent).

## Frozen decision charter (Phase 12 precommit — fixed BEFORE evaluation)

"Materially reduces work beyond B" means, on the frozen workload/series below:
C executes ≥15% fewer TOTAL measured work units (execution + verification +
C's own hashing/traversal/persistence overhead, all metered in-test) than
credible exact-cache B on at least TWO of {1%, 5%, schema-change,
enrichment-change} sparse scenarios, with zero incorrect C terminals and zero
C false reuse. Correctness alone (B verdict failures that C avoids) does NOT
satisfy the work clause; it is recorded separately and can only support
KEEP_VERIFIED_ARTIFACT_ONLY, never CONTINUE. If C ties B on work but the
B→B+ (verification-memory) sensitivity shows the delta lives in evidence
memory rather than the graph, the finding is KEEP_VERIFIED_ARTIFACT_ONLY.
Workload variance is controlled by fixed seeds and exact-count assertions
(matrix JSON), not by sampling statistics.
