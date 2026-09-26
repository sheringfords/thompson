# Thompson Wire Protocol — v1

Status: **frozen** `v1` — thin waist `select`/`record` + `Snapshot{version:1}` locked.
Validates via `thompson-sim` `trace-replay` + `go/harness` `k6` before SaaS cut.

Freeze gate passed: `hard`/`churn` 2.4–14× `FINDINGS.md:43`, `binarize 187×` `FINDINGS.md:114`, `drift 0.999` 5.9× `FINDINGS.md:206` reproduced on `traces/*.jsonl` `trace.rs:1` + `k6` `load/k6.js` p95 `health<100ms` `snapshots<200ms`.

## Thin Waist (2 calls)

```rust
let provider = policy.select(&mut rng)?; // non-mutating `policy.rs:233`
policy.record_outcome(&mut rng, &provider, &Outcome::new(320.0,true,0.0012).with_quality(0.87))?; // `policy.rs:473`
```
```go
provider, _ := policy.Select(rng) // `go/thompson/policy.go:367`
policy.RecordOutcome(rng, provider, thompson.NewOutcome(320,true,0.0012).WithQuality(0.87))
```

## Wire Types

- `Outcome{latency_ms,success,cache_hit,cost_usd,quality:Option<f64>}` `reward.rs:13` / `go/thompson/reward.go:4` — `quality` `clamp01` `Nan→0` `reward.rs:159`, `RampDown` `INF→0` `reward.rs:173`.
- `Config{update_rule,reward_policy,warm_start,selection,discount}` `policy.rs:59` — `Selection::{Thompson,UcbRegularized,Phased}` `policy.rs:21`, `WarmStart::FamilySimilarity{discount:0.2}` `warm_start.rs:81`, `DiscountPolicy` `discount.rs:17`/`go/thompson/discount.go:1`.
- `Snapshot{version:1,config,arms:Vec<Arm>,total_pulls}` `policy.rs:592` `Version=1` `policy.rs:612` JSON float not bit-exact `policy.rs:577` — compare with `1e-9` tolerance.
- `Arm{ id, posterior:Beta(α,β), pulls, warm_started }` `arm.rs:8`, `Posterior{alpha,beta,pulls}` `posterior.rs:39`.

## Canonical Config JSON (protocol v1, both languages)

Writers always emit the canonical shape; readers accept it plus the
pre-canonical Go shape (PascalCase keys, integer `Kind` enums, absent
`config`). Unknown enum strings and unknown versions fail loudly; unknown
*fields* are ignored by both languages (forward-compatible). Semantics:

- `update_rule`: `{"rule":"bernoulli"}` | `{"rule":"binarize","threshold":T∈[0,1)}` | `{"rule":"fractional"}`
- `selection`: `{"selection":"thompson"}` | `{"selection":"ucb_regularized","c" finite,"until_pulls"}` | `{"selection":"phased","bootstrap","min_pulls_for_exploit"}`
- `warm_start`: `{"strategy":"cold"}` | `{"strategy":"fixed","alpha","beta"}` | `{"strategy":"family_similarity","discount","fallback":{"alpha","beta"}}`
- `discount`: `null` (Go `0`) means stationary; otherwise a factor in `(0,1)`.
- Floats round-trip with up-to-ULP error; snapshot comparisons use tolerance.

Migration: pre-canonical Go snapshots (no `config`) restore under the
default configuration in both languages; nothing previously readable became
unreadable. New writers always include `config`. Proven by
`protocol/testdata/` fixtures and the `TestProtocol*` (Go) +
`crates/thompson-sampling/tests/protocol.rs` (Rust) suites, gated in CI via
`go test -run TestProtocol`.

## Sampling Contract

`BetaSampler::sample` `sampler.rs:17` exact via `Exact::gamma` Marsaglia-Tsang `sampler.rs:69` `c=1/√(9d)` `squeeze 0.0331`; legacy `MeanPlusGaussian` etc. `sampler.rs:123` are conformance baselines. `SnapshotStore` `persistence.rs:13`/`go/thompson/persistence.go:1` persists snapshots atomically.

## Conformance

```
cargo test --workspace
cargo test -p thompson-sampling --test protocol   # bidirectional wire fixtures
go test ./thompson/ -run TestProtocol             # same fixtures, Go side
cargo run --release -p thompson-sim -- --seeds 50 --csv docs/results.csv  # `FINDINGS.md:39` sampler table
cargo run --release -p thompson-sim -- --trace traces/*.jsonl           # `trace.rs:1`
go test -race ./...  # `thompson_test.go:579 TestConcurrentUseIsSafe`
```

Freeze gate: real trace replay reproduces `hard`/`churn` 2.4–14× `FINDINGS.md:43` and `graded` `binarize 187×` `FINDINGS.md:114` vs synthetic, plus `drift` `0.999` 5.9× `FINDINGS.md:206` before v1 lock. Contextual `PartitionedPolicy` `context.rs:33` stays partitioned until linear `linear.rs:1` validated.
