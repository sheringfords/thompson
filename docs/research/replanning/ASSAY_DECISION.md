# Assay Decision (Phase 11)

Gates fixed in the mission; applied without moving thresholds.

## Against CONTINUE_TO_VERIFIED_EXECUTION_RUNTIME

1. Correctness/effect gates pass: all R2 finals terminal-ACCEPTED and
   oracle-equal; 8/8 effect boundaries; 9/9 decision-boundary tests;
   held-out 1001–1003 exact.
2. R2 avoids remaining work frozen P3 cannot: lint-switch 13u, arrival 19u,
   overrun 36u, plus rescues where R1 fails outright (verify/op failure).
3. Same mechanism across D1 and D2: one Runtime, one Quote filter, zero
   workload-specific replanning rules (workloads differ only in plan builders).
4. Deterministic and auditable: replay-identical histories (contradictory,
   oscillation, 100-replan, crash-resume pairs); every switch records
   trigger + quotes + rejections; dropped triggers recorded.
5. Effect model prevents invalid switching: scope-conflict and UNKNOWN
   rejections proven; double-apply impossible (quote filter + op guard +
   at-most-once key accounting); history never rewritten.

## Decision: CONTINUE_TO_VERIFIED_EXECUTION_RUNTIME

Retained negatives: same-suffix cases where R2 ties R1 (doc-rotation,
sunk-cost — reported, not hidden); cold wall-clock still fsync-bound;
arrival/rescue value requires shape alternatives to exist; Quote remains
conservative (blocked-subtree pricing); world rebuild is full-plan
reconstruction (no incremental quote cache — fine at this scale, noted).

## Smallest justified next engineering task

Promote the assay runtime loop into a `VerifiedExecution` primitive behind a
narrow interface (submit job + alternatives + trigger feed → terminal
artifact + audit), still research-gated, with the effect-scope registry as
its explicit admission contract for any real effectful op. Proposed only.
