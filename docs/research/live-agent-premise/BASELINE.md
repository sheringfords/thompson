# Baseline (Phase 0)

Mission: `THOMPSON_LIVE_AGENT_PREMISE_PILOT_V1`. Branch
`research/live-agent-premise-pilot-v1` from PR #36 head (`a837740`) in
isolated worktree `thompson-livepilot`. PRs #29–#36 untouched. No merge,
no deploy. Main `814d2b2` verified; all listed SHAs match mission state.

## PR #36 baseline (unchanged code, green)

`go test ./assay/reasoninggoodput/ -run TestDecisionGates|TestPremiseHeldOut`:
7/7 gates PASS on first run in this worktree; premise held-out recall 1.000.
One observation, no code changes (assay files untouched per mission):
a single-sample wall comparison inside gate 6 flaked under machine load
during setup (T4 1203ms vs T2 820ms with IDENTICAL exec counts to a passing
pair, 447/579ms). Exec-unit metrics were rock-stable across all reruns;
only wall timing jittered. Lesson carried into this pilot: prefer
work-unit/normative metrics over wall clock wherever the frozen protocol
allows, and treat single wall samples as fragile.

## Harness availability (verified, not assumed)

- OpenCode version: `opencode v2.0.18` (`opencode --version`).
- Configured model (`~/.config/opencode/opencode.jsonc`): exactly
  `opencode/muse-spark-1.3-contributor-free` (model + small_model).
- `opencode models` lists `opencode/muse-spark-1.3-contributor-free`
  (alongside other free/paid identifiers — none used).
- Auth: API key stored (`opencode auth list`); normal configured path used.
- Smoke invocation (run #1 of the 20-run cap, infrastructure class):
  `opencode run --format json --model
  opencode/muse-spark-1.3-contributor-free` with a read-only prompt in an
  empty temp dir → `{"type":"text",...,"text":"SMOKE-OK"}`, EXIT 0, no tools
  used, session `ses_f0782c9edffeVe0T38C0mq0po6`.
- Capture without hidden chain-of-thought: VERIFIED — JSON event stream
  exposes session/message/part IDs, tool calls, and result text only;
  no thinking/reasoning parts requested or received (`--thinking` not used).
- No subagents, no paid model, no Zen API-key harness, no secrets touched.
