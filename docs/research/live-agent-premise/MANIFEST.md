# Live Pilot Manifest (Phase 1 — FROZEN)

Frozen 2026-10-01. No task, prompt, oracle, schedule, or gate below changes
after the first scored run. Parser mechanics (capture.go) may evolve during
dev runs; parser version is recorded per run and frozen at v1 before held-out.

## Model and harness (frozen)

- OpenCode v2.0.18, normal configured path (no subagents, no Zen harness).
- Model: `opencode/muse-spark-1.3-contributor-free` (exact identifier from
  `opencode models`; also the configured default).
- Invocation: `opencode run --format json --model <id> "<prompt>"` in a
  freshly materialized fixture worktree. No `--auto` (agent must not need
  approvals for these read/test/write-local tasks; a permission hang counts
  as a harness incident, not a scored failure).
- Timeout 20 min per run; fallback injection at 150 s.

## Run budget (frozen, hard cap 20 — includes every model invocation)

| # | Class | Content |
|---|-------|---------|
| 1 | infrastructure | smoke (done, SMOKE-OK, ses_f0782c9edffeVe0T38C0mq0po6) |
| 2–6 | dev scored (5) | LOCAL#1-conflict, CROSS#1-conflict, DISJOINT#1-conflict, OPAQUE#1-conflict, LOCAL#1-L0 |
| 7–17 | held-out scored (11) | CROSS#1-L0, DISJOINT#1-L0, OPAQUE#1-L0, LOCAL#2×2, CROSS#2×2, DISJOINT#2×2, OPAQUE#2×2 |
| 18–20 | spare | provider-incident retries only (max 1 per incident); unspent if unused |

TRANSPARENT mode only (unmodified prompts, normal tool use). STRUCTURED
condition skipped: no spare budget exists for a second mode, and transparent
is the product-relevant result. Revisit only on early collapse with spare
budget remaining (documented if used).

## Tasks (frozen prompts; SUFFIX identical for all)

SUFFIX: `Make the minimal source change to accomplish it. Afterwards run
`go test ./...` and ensure it passes. Do not commit your changes. Reply with
a one-line summary.`

- LOCAL#1: "Change the sales tax rate in calc/tax.go from 0.07 to 0.09,
  updating the test expectation accordingly."
- LOCAL#2: "Change FormatUSD in format/money.go to render like \"USD 1.00\"
  instead of \"$1.00\", updating the test expectation accordingly."
- CROSS#1: "Add a Code string field to the Item struct in store/item.go, and
  add a function ValidateItem(i store.Item) bool in api/input.go that returns
  false when Code is empty and true otherwise."
- CROSS#2: "Change Discount in calc/discount.go to take a minAmount float64
  as its LAST parameter, clamping discounted results below minAmount up to
  it, and update the caller in calc/cart.go and the test in
  calc/discount_test.go accordingly."
- DISJOINT#1: "Two independent fixes: (a) make Sum in calc/sum.go ignore
  negative amounts (treat them as zero); (b) make LineTotal in
  store/item.go round to cents using math.Round(x*100)/100."
- DISJOINT#2: "Two independent fixes: (a) make ValidateName in api/input.go
  also reject names longer than 40 characters; (b) make FormatUSD render
  negative amounts in parentheses, e.g. ($1.00)."
- OPAQUE#1: "Customers report large carts (3+ items) are overcharged when
  discounts apply. Investigate the cart/discount/tax calculation path and
  fix the minimal defect."
- OPAQUE#2: "Large orders (over $1000) never receive discounts. Investigate
  the discount path and fix the minimal defect."

## Oracles (frozen; run on a COPY of the final worktree + probe overlay)

1. `go test ./...` green. 2. Task probe compiles and passes:
   LOCAL#1 `TotalWithTax(100)==109`; LOCAL#2 `FormatUSD(1)=="USD 1.00"`;
   CROSS#1 `ValidateItem({Code:"X"})==true && ValidateItem({})==false`;
   CROSS#2 `Discount(200,25,50)==150 && Discount(40,25,50)==50`;
   DISJOINT#1 `Sum(-5,10)==10` and `LineTotal({Price:2.675},1)==2.67`;
   DISJOINT#2 `ValidateName(41×"a")==false` and `FormatUSD(-1)` starts with "(" ends with ")" contains "1.00" (tolerant: example shows ($1.00));
   OPAQUE#1 `CartTotal({100,100,100},10)==TotalWithTax(270)`;
   OPAQUE#2 `Discount(2048,25)==1536` (binary-exact operands; float dust would false-fail 2000,10).
   Probe compile failure → INCONCLUSIVE-SIGNATURE (excluded from gate
   denominators, documented; neither pass nor violation).

## Contention schedules (frozen)

One background commit per conflicting run (contents in fixture.go), injected
after 3 observed file-read tool events, else 150 s fallback, else scored
CLEAN if the run ends first (recorded). Mapping: LOCAL#1→local1-premise,
LOCAL#2→local2-premise, CROSS#1→cross1-premise, CROSS#2→cross2-premise,
DISJOINT#1→disjoint-b, DISJOINT#2→disjoint2-a, OPAQUE#1→opaque1-premise,
OPAQUE#2→opaque2-premise. L0 runs: quiescent (no schedule; background
commit pre-applied = final state). All background commits keep tests green
(pinned by TestBackgroundCommitsStayGreen).

## Metrics and gates (frozen)

Premise model: cumulative union per model invocation (manifest §analysis).
Cost priority: exposed cost metadata > per-call wall > call count (source
recorded per run; single source used consistently within each analysis).
SARF, L1/L2/L3 counterfactuals per analysis.go. Decision: the 8 CONTINUE
conditions from the mission Phase 13 (zero violations; ≥40% preserved in
held-out partial-invalidation cases; >1 family; transparent material;
SARF ≥0.30 overall with ≥0.50 in one family; majority mechanical provenance;
fresh-L0 equivalence), thresholds unmoved.

## Addendum A — dev-phase probe hardening (before held-out, 2026-10-01)

Two objective probe-authoring bugs found in dev runs (agent work correct,
`go test ./...` green in all 5 dev runs):
- DISJOINT#1 Sum probe called `Sum(-5, 10)`; real signature is
  `Sum([]float64)`. Fixed to `Sum([]float64{-5, 10})==10`.
- DISJOINT#1 rounding probe expected 2.67; `math.Round` half-up gives 2.68
  for 2.675 (agent implementation matches the frozen prompt). Fixed want
  to 2.68.
Parser frozen at `v1-stream-state` after 5/5 dev runs parsed with zero
UNKNOWN premises and token costs recovered on every slice. No task, prompt,
schedule, gate, or run-cap change.
