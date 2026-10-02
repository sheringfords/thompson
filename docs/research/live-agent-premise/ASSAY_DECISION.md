# Live-Agent Premise Pilot — Decision

## Verdict: CONTINUE_TO_REASONING_TRANSACTION_PROTOTYPE

Against the 8 frozen CONTINUE conditions (mission Phase 13, thresholds
unmoved):

1. Zero accepted stale-premise violations — HOLD. All 10 held-out finals
   pass oracle+probes on current state; every injected change flagged stale
   on exactly the injected file; L3 discards all slices touching stale
   premises. Caveat (honest): background commits were behavior-neutral, so
   no run could have demonstrated behavioral corruption; the gate holds
   mechanistically, not adversarially.
2. L3 preserves ≥40% more than L2 where partial invalidation exists — HOLD.
   Held-out: 0.635, 0.500, 0.589, 0.733 (minimum exactly 0.500 on cross2).
3. Selective benefit in more than one task family — HOLD (LOCAL, CROSS,
   DISJOINT, OPAQUE — four).
4. Transparent material preservation, no annotations — HOLD. Unmodified
   prompts, normal tool use; all premises content-derived (blob OIDs,
   dirset digests). No manual premise input, no hidden-CoT access, no LLM
   judge anywhere.
5. SARF ≥0.30 overall with ≥0.50 in one family — HOLD (0.603 overall;
   five of six tasks ≥0.599).
6. Majority mechanically derived provenance — HOLD (90/93 slices; the 3
   UNKNOWN come from one model-initiated subagent delegation, an identified
   failure mode, not the harness).
7. Fresh-L0 equivalence — HOLD (all pairs green on oracle+probes).
8. (7 listed + zero-violation precondition = 8; all hold.)

No KILL trigger fired. Not INCONCLUSIVE: the required free model path
executed 20/20 invocations with zero harness failures.

## Classification: TRANSPARENTLY_USEFUL

Per Phase 11: the benefit exists without redesigning the agent application.
STRUCTURED mode was not run (no spare budget); it is unnecessary for the
verdict.

## Key negatives (must survive into the prototype design)

- cross2-conflict preserved only 0.500: reading the stale file early
  broadens all downstream slices under cumulative-union. Early reads of
  hot files are the selectivity bottleneck.
- Model-initiated subagent delegation (1/15 runs) blinds parent-stream
  premise recovery entirely. A ReasoningTransaction primitive must define
  delegation as an opacity boundary, not a silent gap.
- Relative-path tool calls (1 run) broke workdir derivation; fixed with an
  explicit override. Production instrumentation must scope paths at capture
  time, not reconstruct them offline.
- Injection commits sweep agent-dirty files (git add -A commits everything).
  Faithful to a racing-committer model, but a prototype must separate
  "concurrent commit" from "own dirty writes" explicitly.
- Stale-accept detection was not adversarially stressed (behavior-neutral
  backgrounds). The prototype assay must include a behavior-changing
  TRUE_PREMISE_CHANGE with a red final oracle to prove the guard fires.

## Smallest justified next task

Design-only: `NEXT_REASONING_TRANSACTION.md` (in this branch). Then a
fresh mission for the prototype assay. No product code in this branch.
