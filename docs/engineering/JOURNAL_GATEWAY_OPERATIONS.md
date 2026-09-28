# Journal Gateway Operations (pilot, T3 only)

Scope: operating the single experimental cost-aware treatment backed by
SQLite (`storage_backend: "journal"`). T0/T1/T2 and default JSONL behavior
are unchanged; this note covers only journal deltas. Parent procedures
(data handling, credentials, single-writer deployment) still apply —
see `docs/design/PILOT_DATA_HANDLING_V1.md`.

## Configuration

| Knob | Value for journal T3 | Misconfiguration behavior |
|---|---|---|
| manifest `treatments[].storage_backend` | `"journal"` (omitempty; absent = JSONL) | unknown value refuses at Boot (`JOURNAL-BACKEND-UNKNOWN`) |
| `JOURNAL_PATH` (gateway env, set by runner) | `<treatment-dir>/journal.db` | journal treatment without it refuses (`JOURNAL-BACKEND-REQUIRES-COSTAWARE` unless cost-aware is mis-set) |
| `COSTAWARE=1` + `ROUTER_MODE=verified` | required, same as JSONL T3 | refuses (same as JSONL path) |
| frozen safety config for the treatment | required | refuses when absent (`has no frozen safety config`) |

Backend selection is frozen per experiment: no mid-run switches, no
automatic SQLite→JSONL fallback. A treatment directory never mixes
authorities — the adapter refuses to open where `decisions.jsonl`,
`outcomes.jsonl`, or `safety.jsonl` already exist, and the journal run
creates none of them (asserted in both directions by
`TestJournalEquivalence`).

## File layout (per journal treatment directory)

| File | Contents |
|---|---|
| `journal.db` (+ `-wal`/`-shm` while running) | the only authority: decision, outcome, and safety rows |
| `assignments.jsonl`, `jobmap.jsonl` | runner-side, same as JSONL runs |
| `evidence.jsonl` | gateway evidence path, same as JSONL runs |
| `outcomes.jsonl.checkpoint.json` | quality/cost checkpoint, same as JSONL runs |

There are no `decisions.jsonl` / `outcomes.jsonl` / `safety.jsonl` files.
`journal.db` is created `0600` in a `0700` directory (same posture as
ledger files). SQLite otherwise inherits the process umask, so the
adapter chmods the file set (`-db`/`-wal`/`-shm`) to `0600` at open and
again at close — verified by `TestJournalFilesPrivate`. SQLite runs WAL + `synchronous=FULL` + `busy_timeout=0`:
lock contention surfaces as a loud error, never a wait.

## Restart / recovery (measured Darwin; Linux pending §5)

- Gateway boot replays one SQLite scan into an in-memory projection
  (~30 ms per 2k rows; ~310 ms at 10k jobs / 20k rows), then serves
  Lookup/Latest/history from memory. Steady-state settle performs no
  table scans.
- Crash between commit and learn is unrepresentable as a half-state:
  the row is either committed (learned on replay) or absent (job
  unsettled). Restart heals by replay with identical results.
- Timeout resolution (runner `resolveTimeout`) reads the journal
  authority through short-lived read handles — never `decisions.jsonl`.

## Failure matrix (journal deltas only)

| Failure | Behavior | Recovery |
|---|---|---|
| `journal.db` missing at boot | open fails, binary won't serve | restore from backup, restart |
| corrupt `journal.db` | open/schema check refuses, won't serve | restore from backup, restart |
| `-wal` present without `-db` (partial copy) | open fails closed | restore the full file set, restart |
| disk full on commit | commit error → 500 + persist-issue flag; health degrades | free disk, restart (replay heals) |
| mixed authorities (jsonl beside journal) | adapter refuses at open | start a new experiment dir; never merge |
| second writer on one journal | SQLite busy → loud error (no silent interleave) | single-writer deployment only |

## Backups

Capture the full treatment directory (`journal.db` + `-wal`/`-shm` +
runner-side files + checkpoint). A bare `journal.db` copied while the
gateway runs can miss WAL content — stop the gateway or use the SQLite
backup API, then copy. Partial restores fail closed at open, never
serve partial history.

## Verification

- `go test ./gateway/journalstore/` — adapter + safety-controller
  lifecycle (deterioration, fail-closed, resume, reopen).
- `go test -run TestJournalEquivalence ./cmd/exp-run/` — frozen
  workload, both backends through real binaries (two 40-job runs).
- `GOOS=linux go build ./...` — cross-compile gate (pure-Go SQLite).
