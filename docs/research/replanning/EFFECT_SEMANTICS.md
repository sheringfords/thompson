# Effect Semantics (Phase 2)

Ops are classified per op identity (generic registry, no workload branches):
`PURE` (default), `IDEMPOTENT_EFFECT` (same key ⇒ same effect; sink dedups),
`IRREVERSIBLE_EFFECT` (history boundary). Compatibility is enforced twice:
(1) suffix-quote filter rejects candidates that would re-apply a committed
scope differently, touch an UNKNOWN scope, or re-execute an executed
effectful key; (2) op-level guards fail nodes that would double-apply or
apply under ambiguity. No compensation transactions exist by design: an
incompatible history yields explicit `NoLegalSuffix`, a first-class negative
result (proven in boundary tests). Unresolved ambiguity is UNKNOWN and blocks
optimization (unknown-scope test routes around it with an empty sink).
