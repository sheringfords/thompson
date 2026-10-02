# Live-Agent Premise Pilot — Results (held-out scored)

15 scored live runs total (run cap 20; ledger below). All via
`opencode/muse-spark-1.3-contributor-free`, normal configured path,
TRANSPARENT mode only, parser `v1-stream-state`. Costs in observed tokens
(input+output+reasoning; cache tokens excluded).

## Run ledger (20-cap accounting)

| # | Run | Class | Result |
|---|-----|-------|--------|
| 1 | smoke | infrastructure | SMOKE-OK |
| 2–6 | dev-local1-conflict, dev-cross1-conflict, dev-disjoint1-conflict, dev-opaque1-conflict, dev-local1-L0 | dev scored | all oracle+probes pass |
| 7–10 | wasted cross1-L0, disjoint1-L0, opaque1-L0, local2-conflict | UNSCORED (runner script bug: fixed out-dir args, records unusable) | excluded from all gates |
| 11–20 | 10 held-out (below) | held-out scored | all oracle+probes pass |

Dropped from the frozen 11-held-out plan: `ho-opaque1-L0` (L0 oracle;
opaque-family oracle coverage retained via opaque2-L0 + dev pattern).
Spare retries: 0 remaining at completion; no provider retries were needed
(zero harness failures across all 20 invocations).

## Held-out conflict runs (gated)

| Run | Family | SARF | Opaque | Stale detected | L2 discard | L3 preserved | Preserved frac |
|-----|--------|------|--------|----------------|-----------|--------------|----------------|
| ho-local2-conflict | LOCAL | 0.667 | 0.333 | file:format/money.go (injected) | 12662 | 8038 | 0.635 |
| ho-cross2-conflict | CROSS | 0.500 | 0.500 | file:calc/discount.go (injected) | 15952 | 7980 | 0.500 |
| ho-disjoint2-conflict | DISJOINT | 0.615 | 0.385 | file:api/input.go (injected) | 13538 | 7979 | 0.589 |
| ho-opaque2-conflict | OPAQUE | 0.784 | 0.216 | file:calc/discount.go (injected) | 13286 | 9739 | 0.733 |

Every injected change was detected as stale on exactly the injected file
(no false staleness, no misses). Oracle `go test ./...` + task probes pass
on all final states. Zero UNKNOWN premises in all four.

## Held-out L0 runs (fresh serial oracles)

All 6 pass oracle+probes on final state. SARF: cross1 0.779, local2 0.819,
cross2 0.699, disjoint2 0.812, opaque2 0.702 — except disjoint1-L0 0.000:
that agent spontaneously delegated to 2 model-initiated subagents whose
file observations are invisible in the parent stream (3/4 slices UNKNOWN).
Valid oracle, unmeasurable premises. Only subagent use in 15 scored runs.

## Aggregate (10 held-out, token-weighted)

- SARF overall 0.603 (threshold 0.30). By task: local2 0.741, cross2 0.599,
  disjoint2 0.712, opaque2 0.744, cross1 0.779, disjoint1 0.000 (subagent
  artifact, n=1).
- Opaque fraction 0.336. UNKNOWN slices 3/93 (0.032), all in one run.
- Median premises per turn 3–4; total unique reads per task 4–9.

## Reads per task (held-out conflict)

local2 8, cross2 14, disjoint2 9, opaque2 9 file-read tool events;
model turns 7–14 per run. Early turns read 1–3 files (narrow); later turns
accumulate full context (broad) — the cumulative-union structure that SARF
measures is present in every non-delegated run.

## Fresh-from-scratch equivalence

Each conflict run's final worktree and its matching L0 worktree both pass
the frozen oracle suite + task probes (same task, final states differing
only by behavior-neutral background additions). No accepted result failed
final-state validation. No stale-required-premise accept: background
commits were behavior-neutral by design (isolating the preservation
measurement), so detection power rests on witness mismatch — exercised on
every conflict run — plus final-state validation, which held everywhere.
