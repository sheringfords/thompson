# Representation Collapse Analysis (Phase 8)

Question: can one VerifiedArtifact replace a separate cache entry, verification
record and provenance link?

## Finding: 3 records collapse into 1 + content bytes

Today a verified computation keeps (1) a cache entry keyed by input, (2) a
verification record (outcome version, verifier identity), (3) a provenance link
(receipt → job → attempt chain). The prototype's `Artifact` record carries all
three roles in one object:

- Cache role: `key_digest` → exact lookup (replaces input-keyed entry, and is
  strictly safer: the naive entry caused 55–77 false reuses per corpus).
- Verification role: `verification` + `evidence_id` + `outcome_job_id/version` +
  `verifier_contract` digest (replaces the sidecar verification record; evaluation
  re-checks it against the authoritative outcome on every hit).
- Provenance role: `receipt_id`, full dep snapshot, `upstream:` edges, `reason`/
  `changed_dep` audit trail (replaces the external provenance link; graph
  traversal needs no second store).

What remains necessarily distinct: the artifact CONTENT BYTES (content store,
bound by `artifact_digest` — separating bytes from metadata lets eviction/index
policy differ), and the AUTHORITATIVE OUTCOME itself (journal/outcome ledger —
the artifact mirrors it, never replaces it; corrections flow journal → artifact).

## Authority map

| Fact | Authority (current) | Authority (with artifact) |
|---|---|---|
| Execution happened | journal / outcome ledger | unchanged (artifact cites `receipt_id`) |
| Result is correct | verifier + outcome version | unchanged (artifact cites, re-checks) |
| Result may be reused | — (ad-hoc cache) | artifact validity (VALID/STALE/INVALID/UNKNOWN) |
| Why invalidated | — (recompute logs) | artifact `reason`/`changed_dep` |

No new append-only authority was created (stdlib JSONL mirror only).

## Deletion / complexity

Adopting the artifact as the single reuse representation deletes: the naive
input-keyed cache path, its (absent) invalidation logic, and ad-hoc provenance
joins at reuse time. Net prototype: ~1.4 kLOC Go (`reuse.go`, `canonical.go`,
`store.go`, `workload.go`, `assay.go`) + tests, zero new dependencies, zero
production files touched. Complexity NOT moved into adapters: dependency
declaration is a flat `(name, digest)` list per workload (W1: 8 fields, W2: 7);
no per-workload invalidator code exists — `Evaluate`/`Propagate*` are fully generic.

## Consumers without the optimizer

Any execution system can PRODUCE the contract (canonical key + digests + outcome
citation) or CONSUME it (lookup + evaluate + verify bytes) without Thompson's
router/optimizer: the interface is digests-in/validity-out. Portability limit:
consumers must honestly declare deps — systems that cannot enumerate correctness
deps cannot use it (see decision memo: declaration burden is the binding constraint).
