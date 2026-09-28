# Journal Gateway Equivalence (Phase 3)

## Method
One frozen 40-job supervised workload through real T3 binaries on each
backend (JSONL vs journal), same manifest seed, assignment seed, and
policy seed. Comparison joins through the jobmap (gateway job IDs are
random per run and never directly comparable); assignments use manifest
IDs directly. Only storage-specific metadata may differ.

## Results
40-job supervised workload, both backends through real binaries:
- Assignments: byte-identical (40/40 jobs, same treatments).
- Per-manifest selections: identical arms on every decided job.
- Outcome versions per manifest job: identical (incl. corrections).
- Safety event sequences: identical (monitor + operator demo events).
- Verdicts: NOT_RANKABLE on both (missing-cost gates fire on the
  drift/unmetered mix) — refusal behavior preserved, not just outcomes.
- Backend exclusivity verified both directions (no journal.db beside
  JSONL and vice versa).
- Restart path: journal side reboots via checkpoint+ledger replay
  (covered by adapter recovery tests + supervised resume suite).

## Normalization record
Jobmap-joined IDs only. No normalization of decisions, settlements,
safety state, or statistical conclusions — any difference there fails
the test rather than being papered over.
