# Assay Decision (Phase 9)

Gates fixed in the mission; applied without moving thresholds.

## Analysis (value separation, as required)

- **P1 vs P2** (DAG execution + dedup, reuse value excluded): identical executed
  sets on all D2 scenarios; D1 wins exactly the outside-closure nodes
  (summary under attest-terminal: warm 1→0 exec, cold 7→6). Dedup itself lives
  in exact keys + the shared store (P1 already executes shared keys once).
  P2's incremental value = closure scoping + blocking/ordering audit. Modest
  but real where plans contain non-terminal nodes.
- **P2 vs P3** (plan selection, reuse value excluded): D1 cold direct 20u vs
  staged 23u; D2 cold direct 25u vs staged 37u; D2 lintcfg partial-warm direct
  0 exec vs staged 3 exec/8u. P3 ties P2 when staged is already optimal (warm,
  summary-op). Selection value is measurable in predeclared cold and
  asymmetric-warmth classes. Reuse savings never attributed to the planner
  (matrix separates units-per-treatment).
- **Same planner, both workloads**: one `Quote`/`ChoosePlan`, zero workload
  branches; D1+D2+fan selections all correct with recorded reasons.
- **Overhead vs avoided**: quotes µs-scale; avoided work ms-to-hundreds-ms.
  Overhead exceeds savings only with zero repetition (cold wall-clock: fsync
  writes make P2-cold slower than P0 — reported, not hidden).

## Decision: CONTINUE_TO_RUNTIME_REPLANNING_ASSAY

All five conditions hold: gates pass; P2 reduces work where structure permits;
P3 beats P2 in predeclared classes; planner is generic; overhead is small where
it wins. Negative results retained: P1==P2 on D2; dedup ∈ store not DAG;
cold wall-clock favors P0; Quote over-estimates D1-warm (3u vs 0u executed).

## Smallest next task

Runtime-replanning probe: mid-run invalidation (dependency change AFTER plan
start, BEFORE terminal) — can the executor re-quote and switch physical plans
without restarting the run or violating closure validity? Bounded to D2
lintcfg-change-during-staged-run. Proposed only, not implemented.
