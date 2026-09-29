# Mid-Run Invalidation and Artifact Arrival (Phases 5–6)

## Invalidation (D2 lint/config)

Single-alternative run, lintcfg rotated after compile: lint re-executes under
a new key (new bytes ≠ old bytes — hard gate holds: no old-state artifact
satisfies a new-state node), exact closure `{lint, aggregate, attest}`
recomputes, `validate/compile/setups/unit` reused, final == staged oracle,
spent 33, wasted 0. Two-alternative run (+direct reprieve): one switch
`d2-staged → d2-direct`, R2 spent 20 (wasted 9: abandoned compile) vs R1
frozen 33. R0 final-world oracle agrees with R2's choice and bytes.

Two genuine bugs found by this experiment (fixed, documented): (1) continuing
a stale plan OBJECT after a world change re-executes nodes with stale input
bindings — the runtime now always adopts freshly rebuilt objects (frozen mode
adopts the same-ID rebuild); (2) shared fixture maps leaked trigger mutations
across R1/R2 comparisons — `NewRuntime` deep-copies the world.

## Arrival (independent VALID direct-check artifact mid-run)

R2 switches staged→direct, never produces the arrived key (no duplicate
production asserted), R2 spent 14 vs R1 33. Arrival changes the optimal
SHAPE — the only way it beats a store that already skips available nodes.

## Raw evidence

`go/assay/replan/testdata/replan_matrix.json` rows lint-switch, arrival.
