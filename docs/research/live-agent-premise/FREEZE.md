# Freeze Receipt (Phase 1)

Frozen before the first scored live run (dev run 1):

- Manifest commit: `e93ca91481a32eab3e0dce3d3207837928f4f0e8`
- `MANIFEST.md` sha256: `5dad2d0ca6a4cc4f1b42398b12fafc1d043817313abb1fe30d80ffd633cc82a7`
- Scope frozen: 8 tasks, 8 background commits, 20-run cap ledger
  (1 smoke done + 5 dev + 11 held-out scored + 3 spare), oracles, gates.
- Parser at v0-skeleton; parser version recorded per run; v1 freeze required
  before held-out runs.
- Run ledger: `runs/` directory (created on first scored run), one
  subdirectory per run with stream.jsonl, run.json, repo-meta.json.
