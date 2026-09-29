# Resolver API (Phase 11, P1)

The assay implements exactly the proposed interface; nothing more. No server,
no workflow engine, no effects, no routing/planning concepts. The package
imports only the standard library — resolver independence from Thompson
Sampling is structural (no thompson imports exist to remove).

| Proposed | Implemented |
|---|---|
| `resolve(computation_key, required_verification_key)` | `(*Resolver).Resolve(ck, vk, liveComp, liveVerify, authority) → Decision{REUSE\|VERIFY\|RECOMPUTE\|UNKNOWN, reason}` |
| `record_artifact` | `(*Store).PublishArtifact(key, bytes, receipt, cost)` (idempotent; same-key distinct-bytes refuses) |
| `record_verification` | `(*Store).RecordClaim(vk, status, evidence, authVersion)` (append-only history) |
| `revoke_verification` | `(*Store).RevokeClaim(keyDigest, supersededBy)` (bytes untouched; idempotent) |

Every call returns a machine-readable reason; UNKNOWN fails closed; the
resolve→act protocol is locate-artifact → bind-required-key → resolve →
(REUSE nothing / VERIFY integrity+verify / RECOMPUTE produce+verify).
