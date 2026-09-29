# Extraction Baseline (Phase 0)

Mission: `THOMPSON_ARTIFACT_RESOLVER_EXTRACTION_V1`. Extraction, not expansion.
No merge, no deploy, no production activation.

## Verified topology (fetched 2026-09-30)

- `origin/main`: `814d2b29f66274a36451e740909a4db0e76b26f2` (unchanged).
- PRs #29–#33: all OPEN at mission-stated SHAs (#29 `4f69963`, #30 `b99d001`,
  #31 `42d2120`, #32 `794d7ce`, #33 `d511e85`). No prerequisite merged.
- New worktree `/Users/wira/Documents/thompson-resolver` created DIRECTLY
  from `origin/main`; new branch `feat/artifact-resolver-v1`. NOT branched
  from PR #33; nothing merged from the research stack. PR #33 used only as
  source material (read-only reference worktree `thompson-verify`).
- Toolchain: `go1.27.1 darwin/amd64`; `go/go.mod` declares `go 1.22`.

## Main baseline (pre-extraction, `-count=1` where shown)

- `go build ./...`: OK. `go vet ./...`: clean.
- `go/thompson`, `go/outcome`, `go/gateway/journalstore`: PASS.
- Full `go test ./...` runs in Phase 10 (clean-main integration).
