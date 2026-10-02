# Real-Replay Economics Assay: Results and Decision

Protocol: `PROTOCOL.md`, frozen at `7a89b09` before the first scored run and
not edited since. Machine-readable evidence: `results/*.json` (per
scenario), `results-summary.json`, `results/commit-race-probes.json`,
`decision.json`, `invocations.jsonl`, and raw per-run records under `runs/`.

## Decision: `KILL_INCREMENTAL_REPLAY_ECONOMICS` (Guard mode retained)

`realreplay.Decide` output (`decision.json`): rule 2 fired first, because
H1/H2 R2 and R0 were never committed (§5). **The verdict does not depend on
that rule.** With the HTTP correctness failures set aside:
- rule 3 (a qualifying win in both families) fails: G1 loses in both
  repetitions and H1 is not a win;
- rule 4 (KEEP_GUARD_ONLY: guard holds and some qualifying scenario has a
  positive primary reduction *and* a preserved post-first call) also fails:
  H1's positive reduction came with zero post-first preservation, and G1's
  reductions are negative.

So every path through the frozen procedure ends at KILL. Guard evidence
holds (§6), so commit-time native-witness validation is retained as Guard
mode. The incremental-replay product claim is killed.

## 1. Run accounting

- OpenCode `v2.0.18`; model `opencode/muse-spark-1.3-contributor-free` on
  every invocation; subagents denied; no paid model; no `--thinking`.
- **19 of 20 invocations used:** 2 unscored smokes, 7 Git rep-1, 7 HTTP
  rep-1 (including both H1x rounds), and 3 for the pre-registered Git G1
  repetition. There were zero harness/provider failures and zero retries.
  One invocation is unused (the HTTP H1 repetition needed 3).
- The first smoke-2 attempt failed in `session import` before any
  `opencode run`, so it is not an invocation (PROTOCOL §12).

## 2. Scenarios and exact mutations

| ID | Family | Mutation (exact content in `fixtures.go`) | Role |
|---|---|---|---|
| G1, G1b | Git | `pricing.Discount(price, pct)` → `Discount(price, bps)` (`1 - bps/10000`); test updated | qualifying (late premise) |
| G2 | Git | `format.FormatUSD` adds thousands separators; test updated | early-premise control |
| G3 | Git | `docs/NOTES.md` adds a line | unrelated control |
| H1 | HTTP | `/pricing/widget` `unit_price` 40.0 → 52.0 | qualifying (late premise) |
| H2 | HTTP | `/policy/discounts` gold 10 → 15 | early-premise control |
| H1x | HTTP | H1, then `unit_price` → 61.0 right after the replay's first pricing GET | second change during replay |

Every semantic mutation is genuinely semantic. The S0 result judged on S1
fails the S1 oracle in G1, G1b, G2, H1 and H2 (G1: `LineTotal(10,2,25)` =
19.95, want 15; H1: total 108, want 140.4; H2: discount 10 → 15). The G3 S0
result passes on S1, as an unrelated change should.

## 3. R0 / R1 / R2 comparison (provider-reported tokens)

NMW = new model work = uncached input + output + reasoning. "excl" excludes
every fresh first call (two for R1, one for R2). R1 = S0 + R0;
R2 = S0 + resume round(s).

| Scenario | S0 calls | First stale | Preserved (post-first) | Replayed calls R1 / R2 | NMW excl R1 → R2 | **Primary reduction** | Incl. first | Visible R2/R1 | Win |
|---|---|---|---|---|---|---|---|---|---|
| **G1** | 9 | 2 | 2 (1) | 10 / 7 | 9,072 → 18,103 | **−99.5%** | −5.6% | 0.89 | no |
| **G1b** | 10 | 2 | 2 (1) | 10 / 8 | 22,011 → 29,240 | **−32.8%** | +0.9% | 0.92 | no |
| G2 | 9 | 1 | 1 (0) | 10 / 9 | 9,932 → 16,994 | −71.1% | +1.9% | 0.95 | no |
| G3 | 9 | 7 | 7 (6) | 11 / 3 | 10,871 → 17,607 | −62.0% | +8.1% | 0.61 | no |
| **H1** | 6 | 1 | 1 (0) | 10 / 11 | 67,482 → 35,550 | **+47.3%** | +48.0% | 0.92 | no |
| H2 | 6 | 1 | 1 (0) | 9 / 8 | 33,682 → 44,064 | −30.8% | −4.8% | 1.33 | no |

The raw dimensions per treatment (uncached input, cache read, cache write,
output, reasoning, for all calls and excluding first calls) are in
`results-summary.json` (`r1`, `r2`). Reasoning tokens were reported on every
scored call. The one recorded exception is smoke-1's final call: its
`calls.json` predates the usage-from-export fix, and its usage
(821/44/102) is in `export.json`, checked by
`TestUsageFromExportFillsFinalCall`. Cache writes were 0 on every call.

**Actual preserved model calls (summed over scored scenarios):**
G1 2, G1b 2, G2 1, G3 7, H1 1, H2 1. Post-first preserved: 1, 1, 0, 6, 0, 0.
**Actual replayed model calls:** R2 resume rounds 7, 8, 9, 3, 11, 8. R1
restarts 10, 10, 10, 11, 10, 9.

**Actual goodput** (accepted NMW / executed NMW, all calls): R1 0.45, 0.37,
0.47, 0.51, 0 (not committed), 0. R2 0.84, 0.59, 0.80, 0.98, 0, 0.
Goodput favors R2 even where R2 executed **more** new work. That is exactly
why goodput is reported but not gated.

## 4. Why replay loses: the continuation forfeits the prompt cache

| Call | Uncached input | Cache read |
|---|---|---|
| Every R2 resume's **first** call (8 resumes) | **7,676 – 11,474** | 0 or 1,521 |
| R0's call 1 (all 6 R0 runs, after the excluded first call) | 247 – 670 | 8,945 |
| S0's call 1 | 259 | 8,945 |

The mission requires a continuation built from observable state only. Removing the
provider-encrypted reasoning items (and provider item state) changes the
prompt immediately after the task message. The resumed session therefore
resends the system prompt and the preserved transcript as **uncached** input
(an ~8–11k-token prefill) on its first call, and that call is never
excluded. A fresh restart, by contrast, pays its prefill on a first call
that the primary metric excludes, and then runs warm on the cache at a few
hundred uncached tokens per call.

G3 isolates this. 7 of 9 S0 calls were preserved and only the opaque
`go test` call was invalidated. R2 replayed 3 calls against R1's 11 and used
0.61× the visible tokens. Yet it executed 62% **more** new model work, because
its resume prefill (11,474 uncached) exceeds R0's entire post-first
trajectory.

**Excluding vs including the first call:** including fresh first calls,
reductions are −5.6% to +8.1% for Git. Excluding them (the primary metric)
makes R2 strictly worse. The #37 first-call artifact runs in R1's favor here,
not R2's.

**H1's +47.3% is not a replay win.** Only call 0 was preserved (zero post-first
preservation), R2 made *more* replayed calls (11 vs 10), and the margin comes
from R1's R0 run suffering provider cache misses (64k uncached excluding first
calls vs 31k for R2). The HTTP S0 itself shows a cache miss on call 1
(9,263 uncached, cache 0). Prompt-cache behaviour is not deterministic across
runs. That is a further reason the economics are unstable.

## 5. Correctness and final-state equivalence

| Scenario | R2 committed | R2 oracle | R0 committed | R0 oracle | Stale S0 fails S1 | Missed stale |
|---|---|---|---|---|---|---|
| G1 | yes (CAS) | pass | yes | pass | yes | 0 |
| G1b | yes | pass | yes | pass | yes | 0 |
| G2 | yes | pass | yes | pass | yes | 0 |
| G3 | yes | pass | yes | pass | n/a (unrelated) | 0 |
| H1 | **no** | n/a | **no** | n/a | yes | 0 |
| H2 | **no** | n/a | **no** | n/a | yes | 0 |
| H1x | yes (round 2) | pass (61.0) | n/a | n/a | n/a | 0 |

- **Git:** R2 and R0 converge to the same deterministic oracle (full suite plus
  probes on the committed tree) in all four Git scenarios. Zero stale premises
  reached an accepted result.
- **HTTP:** in the scored runs the agent fetched through tools that the frozen
  classifier does not cover. `execute` is agent-written JavaScript with
  `fetch()`; `search` also appears. The smoke runs had used `webfetch`.
  Per PROTOCOL §4 these observations are UNKNOWN and never validate, so the
  guard refused to commit R2 **and** R0 in H1 and H2. Non-binding check:
  the `quote.json` each wrote (recovered from its `write` tool input) has the
  correct S1 values in all four (H1: 52 × 3 × 0.9 = 140.4; H2: 15% → 102.0).
  The refusals are a premise-coverage gap of transparent capture, not
  wrong answers. They were not reclassified after the fact.
- **Final-state equivalence:** holds wherever a result was accepted.

## 6. Guard evidence (holds)

- Every semantic mutation was detected at validation. Git (G1, G1b, G2):
  stale blob OIDs on exactly the changed files. HTTP (H1, H2): detected via
  UNKNOWN (unattributable `execute` reads), not via an ETag mismatch.
- Commit-race probes, all rejected by the authorities: Git CAS
  (`update-ref` expected-old mismatch), HTTP premise precondition (412), HTTP
  target `If-Match` (412).
- **H1x:** the second change landed during the replay. Round-1 validation
  found the result stale, so it was not committed. Round 2 resumed from the
  new first stale call, read 61.0, and committed correctly. The round-1 trigger
  was UNKNOWN conservatism (pricing was read inside `execute`), not a witness
  mismatch on the second write: the decision was conservative-correct, not
  precise.
- Zero stale-premise accepts in the whole assay.

## 7. False conflicts, UNKNOWN, opacity

- **R1 (broad OCC) false conflicts:** discarded S0 calls with no stale premise
  before them: G1 3, G1b 3, G2 2, G3 8 (all but the opaque one), H1 2, H2 2.
- **R2 opaque-forced replays:** G3 1 (`shell` `go test ./...`). H1/H2 4 each
  (UNKNOWN `execute`).
- **UNKNOWN premises** in accepted sets: Git 0. H1 3, H2 4 (all `execute`
  or `search`). The UNKNOWN rate in HTTP was high enough to block every commit.
- The early-premise controls behaved as designed (G2, H2: first stale call =
  1, nothing post-first preserved).

## 8. Wall time (single observations, secondary)

Agent wall time, R1 vs R2 (s): G1 56.8 vs 76.4; G1b 63.4 vs 72.0; G2 64.2 vs
57.9; G3 69.0 vs 53.1; H1 90.8 vs 98.7; H2 72.0 vs 69.7. The differences are
within the run-to-run spread of the same treatment, so no conclusion is
drawn.

## 9. Anti-artifact checks

| Check | Result |
|---|---|
| First-call prefill removed | Primary excludes all fresh first calls. R2 still loses in every Git scenario. |
| All cached input counted | Visible-token gate passes for Git (0.61–0.95). The loss is in *uncached* new work, the honest cost of the resume. |
| Early true premise | G2 and H2: nothing post-first preserved. Replay degenerates to restart plus prefill. |
| Late premise | G1/G1b: the agent read `pricing/` at call 2 despite the task's step order, so only 1 post-first call survived. |
| Unrelated change | G3: preserved 7/9. Only the opaque `go test` forced replay. Still a net loss. |
| Opaque call consumes change | `shell` and UNKNOWN tools forced conservative replay (G3, H1, H2). |
| Prompt cache cannot manufacture a win | It cannot. The cache works *against* the observable-only continuation. H1's positive number comes from R1-side cache misses, not preservation. |
| Identical output counted as replay | Every executed resume call counted. |

## 10. Negative findings

1. An observable-only continuation (required: no hidden reasoning) forfeits
   the provider prompt cache, and its prefill exceeds the work it saves on
   these task sizes.
2. Real agents read the dependency early even when the task orders it late
   (G1, G1b: call 2). Late-premise preservation is not under the task's control.
3. Coding agents run whole-repository commands (`go test ./...`). Under a
   conservative premise model these are opaque, and any change invalidates
   everything after them (G3).
4. Transparent capture has a tool-coverage problem. The same model switched
   between `webfetch` (smokes) and code-execution `execute`/`search` (scored
   runs). Unclassified tools are UNKNOWN, which blocks commits entirely.
5. Provider prompt-cache hits are not deterministic across runs, so token
   economics on short tasks are noisy (H1).
6. The resumed agent re-orients: resumes made as many calls as, or more than,
   the discarded S0 tail (smoke-2: 6 vs 3).

## 11. Smallest justified next task

No ReasoningTransaction prototype (no `NEXT_REASONING_TRANSACTION.md`; phase
14 is conditional on CONTINUE). The justified next step is **Guard-mode
coverage**: measure, on transparent agent runs, what fraction of
observations commit-time native-witness validation can attribute. That means
classifying code-execution (`execute`) and `search` tools as conservative
opaque premises, rather than UNKNOWN, under a fresh pre-registration, then
measuring the refusal and false-conflict rates. That is the only property
this assay showed working.
