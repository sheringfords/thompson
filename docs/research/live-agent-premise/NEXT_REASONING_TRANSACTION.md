# Next: ReasoningTransaction Prototype (design only, no implementation)

Conditional on CONTINUE (held 2026-10-01). Smallest runtime-neutral API
that the live pilot justifies.

## Modes (separate from day one)

- Guard mode: commit-time premise validation only (witness re-check of
  declared resource set; accept/reject, no replay). Justified even if
  selectivity later disappoints elsewhere — validation never failed here.
- Incremental mode: selective slice replay on partial invalidation.
  Justified by 0.50–0.73 held-out preservation in TRANSPARENT runs.

## Smallest API sketch (to be pinned by the prototype mission, not here)

- `Begin(resources[]) -> Txn` — declares the witnessed read set up front
  (content OIDs, not paths-alone).
- `Slice(txn, name, premises[]) -> Slice` — narrows a computation unit to
  a premise subset; default is the full set (broad OCC fallback, always
  available so adoption never silently over-claims).
- `Validate(txn) -> ok | stale[list]` — re-reads native witnesses;
  stale names exactly the moved resources.
- `Replay(slice) -> result` — reruns only the stale slice; preserved
  slices are returned, never re-executed.
- Delegation rule: spawning a sub-context without premise forwarding
  marks the slice OPAQUE (broad invalidation), per the pilot's 1/15
  delegation finding — never UNKNOWN-silent.

## OpenCode-specific instrumentation to remove for runtime neutrality

- Replace JSONL stream scraping with a tool-proxy boundary: premise
  capture at read-call time (path + bytes + OID in one record), not
  offline output reconstruction (`stripLineNumbers` must not survive).
- Workdir scoping at capture (fixes the relative-path reconstruction).
- Token-cost accounting via native usage metadata, with a call-count
  fallback when metadata is absent (both were exercised; metadata won).

## Next validation workload (non-coding, per mission)

One HTTP/MCP workload: agent reads 2–3 endpoints, aggregates under a
frozen JSON oracle, with one endpoint's ETag rotated mid-run. Predicts:
SARF >0.3 and ≥40% preservation carry over, because endpoint reads are
independent (no cumulative-union pressure except shared auth/config reads).

## Explicitly out of scope

Product code, router/gateway changes, scheduler, lock service, LLM judge,
paid models. The prototype assay reuses only `go/assay/*` harness patterns.
