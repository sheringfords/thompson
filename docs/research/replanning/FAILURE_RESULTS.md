# Failure Replanning Results (Phase 7)

- Verifier rejection (lint): staged illegal → R2 rescues via direct (55u,
  accepted); R1-frozen fails explicitly, terminal rejected, 22u wasted —
  retained as a first-class failure, not a hang.
- Op failure / executor down (lint): same rescue shape via failed-op exclusion.
- Cost overrun (aggregate 60u): R2 switches (55u) vs R1 pays spike live (91u).
- Budget cut to 35 (with direct reprieve): switch to direct; cut to 5:
  explicit `NoLegalSuffix` naming both rejections with spent preserved.
- Revoked reuse (compile outcome revoked mid-run): PURE recompute via
  republication, 0 switches, final == staged oracle.

In every rescue the final is terminal-ACCEPTED and byte-equal to the
appropriate oracle (same-plan P0, or final-world P3 choice for the chosen
shape). No rescue consumes a revoked, failed, or over-budget node.
