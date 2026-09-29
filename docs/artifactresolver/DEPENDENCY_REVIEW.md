# Dependency Review (Phase 6)

Method: `go list -deps ./artifactresolver/` plus import and concept grep
over the public package and API docs (evidence in commit; rerunnable).

## Result

- No dependency on `go/assay/plan`, `go/assay/replan`, materialization,
  gateway routing, sampling, outcome, journal, or any research workload
  code. No thompson-package imports at all in non-test code.
- No third-party modules: the transitive closure is standard library only
  (`crypto/sha256`, `encoding/json`, `os`, `sync`, `time`, plus runtime
  internals). `go.mod` unchanged by this extraction.
- No runtime state machine recreated: the package has no run loop, no step
  function, no pending queue, no retry/backoff, no scheduler. `Resolve` is a
  pure function of (keys, live worlds, authority view); `Store` is a record
  log. Triggers, schedules, and progress files do not exist here.
- Concept search (Action, Run, Proof, approval, effect, planner, arm, router,
  workflow, scheduler, Thompson policy): zero occurrences in public types,
  functions, or contract/API docs except self-descriptive anti-goal lines
  ("No workflow execution…", "depends on no … machinery"). No `Run` type or
  `Action` concept exists.

## Why each dependency exists

`crypto/sha256` (content identity), `encoding/{hex,json}` (digests, durable
log), `os/path/filepath` (file store), `sync` (single-writer safety),
`time` (wall-clock record timestamps), `bufio/strings` (replay scanning),
`errors/fmt/sort` (fail-closed errors, deterministic ordering). Each is
stdlib; none is replaceable by less.
