# Product Boundaries (Phase 11)

Thompson owns exactly one question: **what computation is still necessary,
valid, reusable, stale, or economically preferable?** Vocabulary check on the
assay's public surface (`ExecutionKey`, `VerifiedArtifact`, `Materialized-
Computation`, opportunity/frontier/break-even/counterfactuals): no Action,
Run, Proof, approval, reconciliation, sandbox or tool-policy concepts appear
anywhere. Stop conditions respected: no approval/effect semantics built, no
sibling system touched, no workflow-runtime features added.

- **Igris/Overture** (durable authorization, execution, recovery, proof of
  consequential work): Thompson never authorizes, executes effects, or proves
  them. Overture could FEED Thompson execution receipts + evidence digests as
  dependency inputs (e.g. "effect E applied under contract K" as a dep digest);
  Thompson answers whether downstream computation must redo.
- **Tesera** (gating consequential calls, signed evidence): Tesera's signed
  verdicts could serve as `evidence_id` bindings for materialized artifacts;
  Thompson does not gate or sign anything itself.
- **Marshall** (tool permissioning/sandboxing): Marshall decides WHAT may run
  and WHERE; Thompson decides what NEEDS rerunning. No overlap: permission
  is not validity.
- **btree** (bounded control-flow structure): btree shapes ALLOWED flows;
  Thompson prices and validates their computational nodes. A btree node id
  could key a materialization scope; Thompson owns neither the flow nor its
  bounds.
- **Why Thompson executes no consequential effects**: the entire value
  demonstrated here (and the failure modes contained) concerns pure,
  re-derivable computation. Nothing in the assay reconciles, compensates, or
  approves external state — by explicit non-goal, and the effect machinery
  from the replanning assay was deliberately NOT reintroduced here.
