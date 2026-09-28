# Journal Gateway Integration V1 (Phase 1 design)

## Authority map (one supervised T3 process)
| Fact | Today (JSONL) | Pilot (journal) | Unchanged |
|---|---|---|---|
| Decisions | decisions.jsonl + Seq | journal `decision` rows, Seq=rowid | evidence DecisionStarted rows stay as export |
| Outcomes | outcomes.jsonl + Seq | journal `outcome` rows | evidence rows stay as export |
| Safety transitions | safety.jsonl + Seq | journal `safety` rows | — |
| Assignments | assignments.jsonl (exp-run) | unchanged (exp-run-owned) | — |
| Quality/cost/monitor/budgets | in-memory + files | in-memory, same code, read via adapters | learner/book/monitor/controller untouched |
| Checkpoints | quality JSON + cost sidecar | still written (compat cache) | recovery order unchanged |

## Adapter package: `go/gateway/journalstore`
One `Backend` owns one `*journal.Journal` and constructs:
- `JournalDecisionStore` → `gateway.DecisionStore` + `DecisionScanner`
  (budget recovery scans journal decision rows).
- `JournalOutcomeStore` → `outcome.OutcomeStore` (Submit/Latest/Events/Len).
- `JournalSafetyStore` → new `gateway.SafetyEventSink` interface
  (Append/Events/Failed/Close); `SafetyController` field retyped from
  `*SafetyStore` to the interface (only production-code change outside
  additive files, mechanical).
- No new coordinator code: atomicity lives in the journal ops
  (decision+budget in one txn; outcome+version in one txn). The Backend
  exists so the three stores share one handle — that sharing IS the
  coordination. Claiming more would be fiction.

## Schema extensions (assay package, additive + tested)
- `Decision`: `score_kind`, `eligible_state`, `cost_per_success` (omitempty;
  assay tests unaffected; old rows replay).
- `OutcomeAttempt`: `executor_id`, `transport`, `latency_ms`,
  `validation`, `input/output_tokens`, `failure_category`,
  `field_corrections` (omitempty); top-level `schema_version`,
  `event_type`, `corrected_at`, `occurred_at`, `verified_at`.
- Safety `Nonce`: empty allowed (append-only, matching current gateway
  semantics); non-empty still dedups.

## Selection, settlement, recovery (unchanged code paths)
ServeHTTP → SelectSnapshot → commit → dispatch → SettleHandler
(ValidateCosts → Submit → learner → book → observer) → checkpoints.
All run unmodified against the interfaces; RuleV3, attribution,
idempotency, and OPE-identity behavior are inherited, not reimplemented.

## Activation and frozen config
- Router binary: `JOURNAL_PATH` set (with COSTAWARE=1 + safety envelope)
  builds the journal backend; absent = JSONL. Mismatch (journal flag
  without book/policy binding and vice versa) fails closed at construction.
- Manifest: `storage_backend` omitempty per treatment (`""`=JSONL);
  recorded + hashed with the manifest (verify old-manifest bytes unchanged).
- Runner: boots journal T3 like safety T3 (per-treatment dirs/ports); changing backends mid-experiment refused by header bind.
- No automatic switching: a SQLite write failure surfaces as 500 with the
  persistence flag, exactly like a JSONL write failure today.

## What is NOT claimed
No atomicity across separate interface calls beyond what one journal
transaction actually encloses; no dispatch atomicity (external effects);
no historical migration; JSONL stays default and fully supported.
