# Journal Treatment Deletion Map (pilot)

Rule inherited from the JSONL path: ledgers are append-only, there is no
per-job erase. Deletion means whole-treatment purge. The assay journal
exposes no row-erase API (`DELETE` appears nowhere in
`go/assay/journal`), so per-record erasure is not a procedure the pilot
can run — same limitation as JSONL, different file set.

## Full treatment deletion (supported)

Remove the entire treatment directory. Complete inventory for a journal
treatment:

- `journal.db`, `journal.db-wal`, `journal.db-shm` (authority; sidecars
  may be absent when the gateway is stopped — delete whatever exists)
- `assignments.jsonl`, `jobmap.jsonl` (runner-side)
- `evidence.jsonl` (gateway evidence)
- `outcomes.jsonl.checkpoint.json` (checkpoint sidecar; name retained
  from the JSONL path even for journal runs)
- `progress.jsonl` at the run root references the treatment's jobs —
  purge the run root too if the treatment was the only one, else the
  run is no longer resume-clean for that treatment (by design: resume
  of a deleted treatment refuses rather than re-creates).

Verify: directory gone; a re-run against the same path starts a new
experiment (no replay, no resume) or refuses when root cursors are
inconsistent. There is no secondary index, cache file, or external
projection to chase: the in-memory replay cache dies with the process.

## Per-job / per-row erasure (NOT supported)

Neither backend provides it. For journal treatments specifically, row
removal would require a new assay capability (tombstones or
vacuum-rewrite) with replay-semantics review — explicitly out of the
pilot. If a customer policy requires per-record erasure, do not accept
the data (same gate as `PILOT_DATA_HANDLING_V1.md` §5).

## Credential rotation

Unchanged: tokens travel via environment only and never land in the
journal. Rotate `SETTLE_TOKEN`/`OPERATOR_TOKEN` and restart; past rows
need no re-auth (safety rows record actor IDs for audit).
