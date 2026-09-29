# R0/R1/R2 Comparison (Phase 9)

Source: `TestReplanMatrix` (`testdata/replan_matrix.json`). R0 = final-world
P3 oracle (isolated). Spent in work units (exec+verify); replanning overhead
in µs (quoting only — fsync publish costs are measured in the plan assay).

| Scenario | R0 | R1 frozen | R2 | R2 value |
|---|---|---|---|---|
| lint-switch D2 | direct | staged 33 | direct 20 (waste 9) | saves 13 post-trigger |
| arrival D2 | staged* | staged 33 | direct 14 (waste 9) | saves 19, skips arrived op |
| verify-failure D2 | staged* | FAILS (22 wasted) | direct 55 | rescue (R1 impossible) |
| doc-rotation D1 | staged | staged 37 | staged 37 | none — same suffix (reported) |
| sunk-cost D2 | staged | staged 33 | staged 33 | none — no thrash on sunk cost |

*R0 has no arrival/failure in its final world, so it stays staged: EqOracle
for cross-shape R2 rows is false BY DESIGN (different verified computation).
Terminal ACCEPTED holds on every non-failure row.

Analysis rules honored: reuse savings not counted as replanning (wasted/
reused columns separate them); only post-trigger avoidance vs R1 counts
(lint-switch 13u, arrival 19u, rescue ∞); same-suffix cases reported
(doc-rotation, sunk-cost); overhead (≈0.3–1.8ms total quoting) never exceeds
saved work in switching cases; the same mechanism runs D1 and D2 with zero
workload-specific replanning code. Held-out seeds 1001–1003: oracle-equal,
terminal-accepted, exactly one switch each.
