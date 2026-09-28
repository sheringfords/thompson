# Verified Artifact Contract V1 (Phase 1)

Research contract for the reuse assay. It does not modify the outcome taxonomy
(`ACCEPTED`, `REJECTED`, `UNKNOWN`, `PENDING` in `go/outcome/outcome.go`).

## 1. ExecutionKey — exact computational identity

An `ExecutionKey` names *the computation that was run*, not its output:

| # | Field | Meaning |
|---|-------|---------|
| 1 | `operation` + `operation_version` | Operation identity and version (e.g. `w1.validate/v3`) |
| 2 | `input_digest` | Canonical digest of the primary input bytes (e.g. repo tree, document) |
| 3 | `deps` | Declared dependency digests: ordered `(name, digest)` pairs |
| 4 | `executor` | Executor/tool/model identity and version, where relevant |
| 5 | `env_digest` | Environment/toolchain digest, where relevant |
| 6 | `verifier_contract` | Verifier identity + contract version digest |
| 7 | `policy_digest` | Execution-policy digest, where policy can affect result semantics |

The key digest is `SHA-256` over a deterministic canonical serialization
(length-prefixed fields, sorted dependency names, JSON with fixed field order —
see `go/assay/reuse/canonical.go`). Two different `ExecutionKey`s must never alias
to one artifact record: lookup is by key digest, and `Publish` refuses a second
publication under an existing key digest unless every byte of the canonical key
and artifact digest is identical (idempotent retry), otherwise `ErrKeyConflict`.

## 2. VerifiedArtifact — result + evidence

| # | Field | Meaning |
|---|-------|---------|
| 1 | `key_digest` | Digest of the producing `ExecutionKey` |
| 2 | `artifact_digest` | Digest of the artifact bytes |
| 3 | `verification` | `ACCEPTED` / `REJECTED` / `UNKNOWN` (copied from the source outcome; only `ACCEPTED` is reusable) |
| 4 | `evidence_id` | Verification evidence identity (verifier run / outcome version it was checked against) |
| 5 | `verified_at` | Verification time (opaque timestamp, never used for validity) |
| 6 | `actual_cost_usd` | Measured execution cost (for economics, never for validity) |
| 7 | `deps` | Full dependency set snapshot `(name, digest)` at execution time |
| 8 | `receipt_id` | Producing execution receipt identity (journal seq / run id) |
| 9 | `invalidation` | `state` + `reason` + `changed_dep` (empty while VALID) |

Artifact bytes and evidence metadata are stored separately but bound
cryptographically: the record carries both digests, and `VerifyBytes` re-hashes
supplied bytes against `artifact_digest` before any reuse decision.

## 3. Dependency classes

- **Primary input** (repo tree, source document): always participates.
- **Config / schema**: relevant config files, lockfiles, extraction schema version.
- **Toolchain / executor**: compiler/test-binary version, model+prompt/config version.
- **Environment**: env inputs the command actually reads (allow-listed, not whole env).
- **Verifier contract**: verifier identity + version. A verifier change never silently reuses.
- **Execution policy**: included where policy can change result semantics; otherwise a
  fixed empty digest with a justification recorded in the workload doc.
- **Non-semantic metadata**: wall-clock timestamps, hostnames, run ids, log formatting —
  explicitly excluded; `WORKLOADS_AND_MUTATIONS.md` lists per-workload exclusions and the
  irrelevant-metadata mutations that must NOT invalidate reuse.

## 4. Reuse-validity states

`VALID` — full key (including current dependency digests) matches and source outcome
still ACCEPTED. `STALE` — a declared dependency digest changed (or verifier contract
changed); the artifact is historically intact but not reusable. `INVALID` — source
verification was superseded by a correction, evidence conflicts, or a required
dependency cannot be resolved. `UNKNOWN` — fail-closed: missing metadata, unknown
outcome version, conflicting evidence. **UNKNOWN is never VALID.**

A VALID→STALE transition never rewrites history: the producing outcome stays ACCEPTED
in the journal; only reuse is refused. Restoring the original dependency bytes does
not auto-revive anything: reuse requires the *current* `ExecutionKey` digest to match
a stored record whose source outcome is still the authoritative ACCEPTED version
(§6: exact-key match, not resurrection).

## 5. Fail-closed rules

1. Any required dependency missing a digest → refuse (`ErrMissingDep`, UNKNOWN).
2. Unknown outcome version / superseded evidence → INVALID.
3. Conflicting verifier evidence → INVALID, never majority-vote.
4. Semantic similarity is not identity: a hit requires byte-exact key-digest equality.
5. Duplicate publication is idempotent only for byte-identical records.
6. Corrections propagate transitively before any further reuse (Phase 6).

## 6. Evaluation semantics

`Evaluate(key, currentDeps, outcomeLookup)` returns `(VALID|STALE|INVALID|UNKNOWN, reason)`:
key-digest miss → no reuse (caller executes fresh); hit → compare stored dep digests
against `currentDeps`, then check the source outcome is still the authoritative
ACCEPTED version. Downstream artifacts name upstream artifact digests as dependencies,
so invalidation and correction propagate transitively by graph traversal.

## Invariants (must hold; tested)

1. No validity from semantic similarity — exact digest equality only.
2. Every reused artifact traces to a previously ACCEPTED verified execution.
3. Any declared-correctness-dependency change invalidates reuse.
4. Non-semantic metadata changes do not invalidate reuse.
5. VALID→STALE never rewrites the outcome to REJECTED.
6. UNKNOWN validity is never treated as VALID.
7. Authoritative corrections propagate into artifact validity (transitively).
