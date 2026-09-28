# Verified Artifact Reuse Assay — Baseline (Phase 0)

Mission: `THOMPSON_VERIFIED_ARTIFACT_REUSE_ASSAY_V1`. Research-only prototype. No merge, no deploy.

## Starting state (exact)

- Worktree: `/Users/wira/Documents/thompson-reuse`, branch `research/verified-artifact-reuse-v1`
- Starting SHA (worktree HEAD == `origin/main`): `17d38682d6009879b0bb45a882b2e7e875ab1004`
  (`Transactional journal assay: SQLite WAL prototype + matched evaluation (no merge) (#27)`)
- Local `main` in the primary checkout is stale (`662b380`); all SHAs above are taken in the
  isolated worktree against `origin/main`. The assay uses the worktree exclusively.
- Canonical repo for PRs: `sheringfords/thompson` (gh context). Local git remote in some
  checkouts points at `wiramahendra/thompson-sampling` (mirror); PR URLs below use the
  canonical owner.
- Journal gateway pilot PR #28: **OPEN**, head `feat/journal-gateway-pilot-v1` at
  `c640415e091661a4b418702cb5b311794a817774`, base `main`, mergeable `MERGEABLE`.
  **Not merged — the assay does not depend on it and does not touch it.**
- Open PRs: only #28. Branch protection on `main`: none (404, not protected).
- Toolchain: `go version go1.27.1 darwin/amd64`; `go/go.mod` declares `go 1.22`.
  New code must stay Go 1.22-compatible (stdlib only + already-vendored `modernc.org/sqlite`).
- Dirty state: none in the assay worktree (`git status --short` clean). Unrelated worktrees
  (`thompson-assay`, `thompson-journal-pilot`, `thompson-pilot`, …) preserved untouched.

## Prior work read

- `docs/research/journal/` (assay decision, baseline/plan, failure-injection, performance,
  deletion/complexity, state inventory, paper findings) — transactional SQLite WAL journal
  is the authoritative-execution-fact model this assay reuses; no new append-only authority.
- `go/assay/journal/` (`journal.go`, `ops.go`, `replay.go`, `validate.go`, `*_test.go`) —
  storage pattern to mirror (WAL + FULL sync, single writer, deterministic replay).
- Journal-gateway pilot (PR #28 branch, worktree `thompson-journal-pilot`,
  `go/gateway/journalstore/`): read-only reference; nothing copied into the assay.
- `go/outcome/outcome.go` — outcome taxonomy ACCEPTED / REJECTED / UNKNOWN / PENDING
  (`SchemaVersion = 1`), versioned superseding corrections. The reuse validity taxonomy
  (VALID / STALE / INVALID / UNKNOWN) is separate and defined in Phase 1.
- `go/bench/` (`corpus.go`, `economics_test.go`, `systems_test.go`, …) and
  `docs/research/{ADAPTIVE_ECONOMICS_RESULTS,BENCHMARK_BASELINE}.md` — known finding:
  persistence/verification/recovery dominate policy-computation cost; adaptive routing
  shows no consistent economic win. This assay optimizes verified work, not routing.

## Baseline test results (before new code)

- `go vet ./...` (in `go/`): clean.
- `go test ./thompson/`: PASS (protocol checks unaffected).
- `go test ./outcome/...`: PASS.
- `go test -race ./assay/journal/ -run 'TestJournal|TestReplay|TestValidate|TestCompare'`: PASS.
- Full `go test -race ./assay/... ./outcome/...`: `TestFaultKillMidCommit` FAILED once under
  parallel race load (`child never reached 50 commits`, 30 s), then **PASSED in isolation**
  (`ok … 2.1s`, recovered prefix of 61 events). Recorded as flaky-under-load, not a blocker;
  final verification re-runs it in isolation.

## Isolation

- All new implementation lives in `go/assay/reuse/`; all docs in `docs/research/reuse/`;
  only synthetic/public fixtures under those trees. No production code changes.
- SQLite is via the already-required `modernc.org/sqlite` (no new dependencies).
