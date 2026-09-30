# Baseline and Prior Evidence (Phase 0)

Mission: `THOMPSON_OUTCOME_LEDGER_DISCOVERY_V1`. Discovery, not construction.
No merge, no deploy. Branch `research/outcome-ledger-discovery-v1` from
`origin/main` (`814d2b2`) in isolated worktree `thompson-ledger`. PRs #29–#34
all OPEN, untouched. Toolchain go1.27.1; go.mod declares go 1.22.

## Main baseline (pre-discovery)

`go build ./...` OK; `go/thompson`, `go/outcome`, `go/gateway/journalstore`
PASS (`-count=1`).

## Prior-evidence inventory (what Thompson already tested)

- Adaptive model routing shows no consistent economic win over competent
  static policies; persistence/verification/recovery dominate compute cost.
- Transactional SQLite-WAL journal carries authoritative execution facts with
  deterministic replay (merged pilot #28 for one isolated treatment).
- Exact evidence-bound reuse is correct (zero false reuse, held-out) but its
  work value over a credible exact cache is ~nil on pure functional
  workloads; the graph layer added cost without eliminating operations.
- Verified-artifact evidence (contract currency + outcome lineage) is what
  the conventional cache lacks: 519 stale-contract + 1 revoked-evidence
  false reuses refused. Decision: KEEP_VERIFIED_ARTIFACT_ONLY.
- Runtime replanning saves work only via shape changes; the store already
  dedups shared keys. Physical-plan selection value is real but narrow.
- Split computation/verification identity (REUSE|VERIFY|RECOMPUTE|UNKNOWN)
  extracted as `go/artifactresolver` (PR #34): contract rotation re-verifies
  without reproduction at 25–4700× overhead coverage in production-dominated
  regimes; marginal near parity.

## Frozen non-directions (not revived because code exists)

Model routing as company thesis; generic verified runtime; DAG
materialization; physical-plan optimizer; runtime replanning; generic agent
eval platform; trace compiler as company thesis; artifact resolver as
standalone company thesis. This mission tests a different question —
whether a durable cross-system economic episode record is missing — and may
use retained primitives only as vocabulary, never as justification.
