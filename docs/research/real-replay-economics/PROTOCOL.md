# Real-Replay Economics Assay: Frozen Protocol

Mission: `THOMPSON_EVIDENCE_CLOSEOUT_AND_REAL_REPLAY_ECONOMICS_V1`.
Starting main: `67dabae783361cdb79aa86a8fd7b8bffc6d9853d` (PR #39 merged).
Harness: `go/assay/realreplay` at commit `641efe9` (`754c66a` plus the
pre-freeze smoke fixes in §12).

This protocol is committed **before the first scored model run** and is not
edited afterward. Results that contradict it are reported against it, not
fixed by changing it. It does not reuse #36's overhead gate or #37's
counterfactual token-preservation metric.

## 1. Agent, model, budget

- Harness: OpenCode `v2.0.18`, `opencode run --format json --auto`.
- Model: `opencode/muse-spark-1.3-contributor-free` (the only model passed on
  any invocation; constant `realreplay.Model`). No paid model, no fallback.
- Subagents disabled for every treatment by the fixture's `opencode.json`
  (`"permission": {"task": "deny"}`). It is identical in every workspace.
- No hidden chain of thought is requested (`--thinking` is never passed),
  inspected, or reused (§5).
- **Run cap: 20 `opencode run` processes in total**, including smokes,
  failures and retries. Every process is logged in `invocations.jsonl`
  *before* launch. The harness refuses to launch the 21st.
- Allocation, in fixed order:
  1. 2 unscored smoke invocations (`smoke-1`, `smoke-2`; done before the freeze).
  2. Git family, rep 1: `S0`, then for G1, G2, G3 each `R2` (if a resume is
     needed) and `R0`. That is at most 7.
  3. HTTP family, rep 1: `S0`, then for H1, H2 each `R2` and `R0`, then the
     H1x adversarial case (up to 2 resume rounds). That is at most 7.
  4. Remaining budget: at most one retry per *harness/provider* failure (a
     process that exits non-zero, times out, or emits an error event).
     Never a retry because a result is inconvenient. Then, if at least 3
     remain, a second repetition of the Git family limited to G1
     (`S0`, `R2`, `R0`). Then, if at least 3 remain, a second repetition of
     the HTTP family limited to H1. The cap is never increased.

## 2. Task corpus (exact text in `fixtures.go`)

**Git task `TaskGit`** (module `shopcalc`):
1. Make `format.FormatUSD` render negatives in accounting style
   (`FormatUSD(-1.5) == "($1.50)"`) and add a test.
2. Then add `pricing/line.go` `LineTotal(unitPrice, qty, discountPct)`
   using the existing `pricing.Discount`, and add a test.
   Then run `go test ./...`, don't commit, and reply with a one-line summary.

**HTTP task `TaskHTTP`** (fixture at `http://127.0.0.1:18741`):
1. Fetch `/customers/c42` and `/policy/discounts` and write `customer.md`
   (name, tier, discount percent).
2. Then fetch `/pricing/widget` and write `quote.json` with exactly
   `{customer, sku, qty: 3, unit_price, discount_pct, total}`.
   Don't modify HTTP resources, and don't commit.

The step order is part of the task text and is identical for all
treatments. No treatment receives any other text, apart from the resume
trigger in §5.

## 3. Semantic mutations (exact content in `fixtures.go`)

| ID | Family | Changes | Why it is semantic | Role |
|---|---|---|---|---|
| G1 | Git | `pricing.Discount` takes basis points instead of percent; its test updated | a `LineTotal` that passes percent is wrong on S1 (probe `LineTotal(10,2,25)==15` fails) | **qualifying** (late premise) |
| G2 | Git | `format.FormatUSD` gains thousands separators; its test updated | S1 probe adds `FormatUSD(-1234.5)=="($1,234.50)"` | control: early premise |
| G3 | Git | `docs/NOTES.md` content | none (unrelated) | control: unrelated change |
| H1 | HTTP | `/pricing/widget` unit price 40 → 52 | quote total 108.00 → 140.40 | **qualifying** (late premise) |
| H2 | HTTP | `/policy/discounts` gold 10 → 15 | customer.md and quote both change | control: early premise |
| H1x | HTTP | H1, then a second write (unit price → 61) applied by the authority right after the replay's first GET of `/pricing/widget` | the replay's own read becomes stale | adversarial: second change during replay |

**Timing:** each mutation lands on the authority **after the S0 agent run
completes and before validation**: a concurrent writer committing during the
transaction's lifetime. Git mutations are commits on the authority's `main`
by another committer. They never touch the agent's workspace. HTTP mutations
are writes to the fixture.

## 4. Authorities and witnesses

- **Git:** a bare repository. `refs/heads/main` is the sole authority. The
  agent works in a non-bare clone at a fixed path, checked out at the
  scenario's base commit. Witnesses are resolved against that immutable base
  commit: **blob OID** for file `read`/`edit`/`write` (writes pin the base
  blob so a concurrent change is never overwritten), a **names projection of
  the native tree** (hash of sorted entry names and types, recomputed from
  the authority at every validation) for directory listings and `glob`, the
  **content tree OID** for `grep`. Commit = new commit whose parent is the
  validated head, then `git update-ref main <new> <validated-head>` (CAS).
- **HTTP:** strong ETag = content hash. The fixture logs every served GET
  with the ETag it served. A `webfetch` premise is bound to the log entry for
  the same path inside the tool's time window (±1 s). No match or conflicting
  versions → UNKNOWN. Commit = `PUT /quotes/q1` with `If-Match` on the target
  plus `X-If-Match-Resources` for every HTTP premise, checked atomically by
  the fixture. (Limitation: real HTTP servers usually offer only the target
  `If-Match`.)
- **Opaque:** `shell`/`bash` → the whole workspace content tree plus every
  HTTP resource at its run-start ETag. Any tool not in the classified set →
  UNKNOWN (never validates). `todowrite`/`todoread` → no external premise.
- Thompson stores premise provenance only. No version counter, epoch, or
  copy of world state is used for correctness.

## 5. Treatments and the replay procedure

The premise model is **cumulative union**: model call *k* depends on every
tool result produced by calls `0..k-1`. With one assistant message per model
call (asserted per run, §8), call *k*'s message carries the tool results of
call *k*.

- **S0:** the agent runs the task on S0 to completion. S0 is shared by
  R1 and R2 of every scenario in the same family and repetition.
- **R0 `R0_FRESH_FINAL_STATE`:** a fresh execution of the identical task on
  the final state S1 (separate authority clone at the identical S1 commit,
  same workspace path). It is the correctness reference.
- **R1 `R1_FULL_RESTART`:** S0 is discarded at validation and the task
  restarts on S1. The restart is exactly the R0 execution, reused because it
  is the same task on the same state with the same model and tools. So
  **R1 work = S0 + R0**.
- **R2 `R2_ACTUAL_SELECTIVE_REPLAY`:** validate all S0 premises against S1.
  Let *j* = the first call whose tool results include a stale or UNKNOWN
  premise.
  - *j* = none: no model call. The S0 workspace is rebased onto S1 and
    committed (`commit-only`).
  - *j* = 0: nothing is preservable. R2 is the R0 execution
    (`restart-equivalent`, no extra invocation).
  - *j* > 0 (`resume`): calls `0..j-1` are preserved and never re-invoked.
    Message *j* is dropped even though its call consumed only valid inputs,
    because it carries a stale tool result. The continuation is built by:
    1. exporting the S0 session and keeping the task message plus the first
       *j* assistant messages;
    2. removing every `reasoning` part (provider-encrypted chain of thought)
       and every `providerState`/`providerMetadata` field, so the
       continuation contains only observable transcript and tool state;
    3. giving it fresh IDs and importing it as a new session;
    4. rebuilding the workspace as OpenCode's own snapshot taken at the start
       of call *j* (the exact file state the preserved calls produced),
       rebased onto S1. A conflict falls back to restart and is recorded;
    5. resuming it with `opencode run --session <new> "Continue."`.
       `"Continue."` is the only extra text any treatment receives. It
       carries no state information and no staleness hint.

    The resumed agent re-executes call *j* onward for real, against S1. At
    the end, every accepted premise (preserved and new) is re-validated
    against the authorities. If anything is stale, the procedure repeats
    from the new first stale call (another real round). The result is
    committed only when everything validates.
- Preserved calls are verified never re-executed: no call in a resume stream
  may carry a preserved message ID.

## 6. Usage accounting (raw, provider/OpenCode-reported)

Per model call, from the `step_finish` event: `input` (uncached input),
`cache.read` (cached input), `cache.write`, `output`, `reasoning` (recorded
as exposed or unexposed and never estimated).

- `new_model_work` = uncached input + output + reasoning.
- `total_visible_model_tokens` = uncached input + cache.read + cache.write +
  output + reasoning.
- **First-call rule:** the first call of every *fresh start* (a run that
  begins from the task prompt alone: S0 and R0) is the initial prefill. The
  primary metric excludes it for **both** treatments, so R1 has two
  exclusions (S0 and R0) and R2 has one (S0). Resume calls are never
  excluded: the resent preserved transcript is billed as reported (cached or
  uncached).
- Every primary metric is reported both including and excluding fresh first
  calls.
- No dollar values. Cache tokens are reported and never dropped or priced.
- Counted as replayed: every model call in a resume or restart, even if its
  output is identical to the original.
- Counted as preserved: an S0 call reused in the accepted result *without*
  another invocation.
- Wall time is secondary evidence only.

## 7. Gates (frozen)

A scenario is **qualifying** if it is G1 or H1 (in any repetition).

**Primary:** `1 − NMW(R2) / NMW(R1) ≥ 0.30`, where NMW is `new_model_work`
summed over every call *executed* in the treatment, excluding fresh first
calls (§6). R1 = S0 + R0. R2 = S0 + every resume round (or S0 + R0 if
restart-equivalent).

**Secondary (all required for a win):**
1. R2 executes fewer repeated model calls than R1
   (`calls(resume rounds) < calls(R0)`).
2. `total_visible(R2) ≤ 1.10 × total_visible(R1)`, over all calls including
   first calls and cached input.
3. R2 preserves at least one **post-first-call** S0 model call
   (preserved calls ≥ 2, i.e. calls 1.. survive, not only call 0).
4. Correctness: R2 and R0 both committed through the authority and both pass
   the final-state oracle, with zero stale premises in the accepted set.
5. Usage telemetry present on every call.

A qualifying scenario **wins** only if every executed repetition of it
satisfies the primary gate and all secondary gates.

Also reported (not gates): recovery-only reduction (resume rounds vs R0
excluding R0's first call), goodput, the discard classification, the
UNKNOWN count, and wall time.

## 8. Correctness oracle and integrity checks

- **Git final-state oracle:** `go test ./...` on the committed tree plus task
  probes for the final state (`FormatUSD(-1.5)=="($1.50)"`,
  `FormatUSD(2)=="$2.00"`, `LineTotal(10,2,25)==15`; under G2 also
  `FormatUSD(-1234.5)=="($1,234.50)"`).
- **HTTP final-state oracle:** `quote.json` has exactly the 6 fields, with
  the values implied by the authority's final state (total to the cent);
  `customer.md` contains the name, the tier and the final discount percent;
  the committed `/quotes/q1` equals `quote.json`.
- **Semantic-change proof:** the S0 result, rebased onto S1, must fail the S1
  oracle for G1, G2, H1 and H2. It is recorded per scenario.
- **Guard evidence:** every semantic mutation is detected stale at
  validation. Commit-race probes (Git CAS; HTTP premise precondition and
  target If-Match) must all be rejected. H1x must produce a second
  validation/replay round and a correct commit, never a silent commit.
- **Run integrity (aborts the scenario as a harness failure if violated):**
  one assistant message and exactly one step per model call, aligned with the
  export; OpenCode's first-call snapshot equals the materialized base; no
  preserved message re-executed.

## 9. Anti-artifact checks

| Check | Mechanism |
|---|---|
| Remove first-call prefill | primary metric excludes every fresh first call (R1 gets two exclusions) |
| Count all cached input | secondary gate 2 uses total visible tokens incl. cache; reported per call |
| Early true premise broadens context | G2 and H2 (expected: little or nothing preserved) |
| Late premise, earlier work survives | G1, H1 |
| Unrelated change | G3 (expected: only opaque `shell` observations force replay) |
| Opaque call consumes the changed resource | shell = whole-world premise; any shell after the change point forces replay |
| Prompt cache cannot manufacture a win | R0 runs in the same workspace path right after R2 and gets the same warm system-prompt prefix; first calls excluded; total visible tokens gated |
| Identical repeated output still counts as replay | every executed call counts regardless of content |

## 10. Decision procedure (frozen; `realreplay.Decide`)

1. Any call without usage telemetry → `INCONCLUSIVE_USAGE_TELEMETRY`.
2. Any stale premise in an accepted result, or any R2/R0 correctness
   failure → `KILL_INCREMENTAL_REPLAY_ECONOMICS`.
3. A qualifying win in both Git and HTTP →
   `CONTINUE_TO_REASONING_TRANSACTION_PROTOTYPE`.
4. Guard evidence holds, and some qualifying scenario has a primary
   reduction > 0 with ≥ 1 preserved post-first call → `KEEP_GUARD_ONLY`.
5. Otherwise → `KILL_INCREMENTAL_REPLAY_ECONOMICS` (Guard mode is retained
   if guard evidence holds).

`ARCHITECTURE_DEPENDENT` applies if the resume could only be made to work by
restructuring the task into Thompson-specific stages. The design above uses
the unmodified task and agent. If a smoke had shown otherwise, the assay
would have stopped here.

## 11. Evidence retained

`invocations.jsonl` (cap ledger), and per run `stream.jsonl` (raw OpenCode
events including raw usage), `export.json`, `calls.json`, `premises.json`,
`http-served.json`, and `continuation.json` plus `continuation-stats.json`
for resumes. `results/<scenario>.json` holds metrics and gates,
`results/commit-race-probes.json` the race probes, and `decision.json` the
decision.

## 12. Pre-freeze smoke findings and harness changes

Two unscored invocations were spent (`invocations.jsonl`: `smoke-1`,
`smoke-2`). The scored budget is therefore 18.

- **smoke-1** (HTTP task on S0): 7 calls; tools `read` ×1 (root listing),
  `webfetch` ×3, `write` ×2. Every fetch was bound to the authority's served
  ETag (no UNKNOWN). One assistant message and one step per call. The
  first-call snapshot equals the base. Under H1 the first stale call is call
  4 (the pricing fetch), so calls 0–3 are preservable.
- **Harness defect 1:** the stream omits the final call's `step_finish`
  when the process exits. Fix (`641efe9`): usage is taken from OpenCode's
  stored assistant message for every call and must equal the stream exactly
  where both report it. It did on all 6 calls of smoke-1.
- **Harness defect 2:** the first smoke-2 attempt failed in
  `session import` (relative path) **before any `opencode run`**, so it is
  not an invocation. Fix: absolute paths. smoke-2 was then run as a resume
  of the recorded smoke-1.
- **smoke-2** (real resume, H1): the continuation kept 4 assistant messages
  and removed 12 hidden items (encrypted reasoning and provider item state).
  The resumed agent made 6 calls, re-fetched pricing at S1, wrote
  `quote.json` with total 140.4, and passed the S1 oracle. The mechanism
  works on the unmodified task and agent, so `ARCHITECTURE_DEPENDENT` does
  not apply.
- **Observed cost, recorded before freezing and not acted on:** the resume's
  first call billed **8,038 uncached input tokens (cache.read 1,521)**. In
  S0, the same position read ~9.4k tokens from cache. Removing the encrypted
  reasoning changes the prompt right after the task message, so the resent
  preserved transcript misses the provider prompt cache. The resume also made
  6 calls where S0's tail had 3. Under §6 and §7 this is real replay cost and
  counts in full. No gate, threshold, task, mutation, premise rule or
  accounting rule was changed in response.
