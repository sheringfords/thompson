//! Protocol-v1 conformance: canonical JSON wire format shared with the Go
//! implementation. These tests read the fixtures in `protocol/testdata/` and
//! prove bidirectional compatibility. They are the trace-replay/CI
//! conformance anchor alongside the Go `TestProtocol*` suite.

use std::collections::BTreeMap;
use thompson_sampling::{Outcome, RewardPolicy, Snapshot, ThompsonSampling};

fn fixture(name: &str) -> String {
    let path = format!("../../protocol/testdata/{name}");
    std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("fixture {path}: {e}"))
}

fn approx(a: f64, b: f64, tol: f64) {
    assert!((a - b).abs() <= tol, "expected {b} (±{tol}), got {a}");
}

/// Go-authored snapshot: full arm set, config, posteriors, pulls, totals,
/// and policy identity must verify, and the snapshot must restore.
#[test]
fn go_snapshot_restores_with_config() {
    let snap = Snapshot::from_json(&fixture("snapshot_go.json")).expect("decode go fixture");
    assert_eq!(snap.version, Snapshot::VERSION);
    assert_eq!(snap.total_pulls, 10);
    assert_eq!(snap.arms.len(), 2);

    let mut by_id = BTreeMap::new();
    for arm in &snap.arms {
        by_id.insert(arm.id.clone(), arm);
    }
    let gpt = by_id["openai/gpt-4"];
    assert_eq!(gpt.pulls(), 6);
    approx(
        gpt.posterior.mean(),
        9.401883326338973 / 10.401883326338973,
        1e-12,
    );
    approx(gpt.cumulative_reward, 4.8, 1e-9);

    // Config identity: binarize/ucb/family/discount fixture.
    let json = snap.to_json().unwrap();
    assert!(json.contains("\"rule\": \"binarize\"") || json.contains("\"rule\":\"binarize\""));
    assert!(json.contains("ucb_regularized"));

    let policy = ThompsonSampling::restore(snap, Box::new(thompson_sampling::Exact)).unwrap();
    assert_eq!(policy.total_pulls(), 10);
}

/// Legacy Go snapshot (PascalCase, no config) restores under the default
/// configuration instead of failing to decode.
#[test]
fn legacy_go_snapshot_restores_with_default_config() {
    let snap =
        Snapshot::from_json(&fixture("legacy_go_snapshot.json")).expect("decode legacy fixture");
    assert_eq!(snap.arms.len(), 1);
    assert_eq!(snap.total_pulls, 3);
    let arm = &snap.arms[0];
    assert_eq!(arm.id, "openai/gpt-4");
    assert_eq!(arm.pulls(), 3);
    approx(arm.posterior.mean(), 3.0 / 5.0, 1e-12);
    let policy = ThompsonSampling::restore(snap, Box::new(thompson_sampling::Exact)).unwrap();
    assert_eq!(policy.total_pulls(), 3);
}

/// The Rust-authored fixture re-serializes to itself: the committed file is
/// canonical Rust output, not hand-shaped JSON the encoder would never emit.
#[test]
fn rust_fixture_is_canonical_encoder_output() {
    let raw = fixture("snapshot_rust.json");
    let snap = Snapshot::from_json(&raw).expect("decode rust fixture");
    let a: serde_json::Value = serde_json::from_str(&raw).unwrap();
    let b: serde_json::Value = serde_json::from_str(&snap.to_json().unwrap()).unwrap();
    assert_eq!(a, b, "fixture is not canonical encoder output");
    assert_eq!(snap.total_pulls, 14);
}

/// Unknown snapshot versions are rejected, not best-effort parsed.
#[test]
fn invalid_version_rejected() {
    let raw = fixture("snapshot_rust.json");
    let mut snap = Snapshot::from_json(&raw).unwrap();
    snap.version = 999;
    assert!(ThompsonSampling::restore(snap, Box::new(thompson_sampling::Exact)).is_err());
}

/// Shared reward vectors agree within 1e-12.
#[test]
fn reward_vectors_match() {
    #[derive(serde::Deserialize)]
    struct Row {
        latency_ms: f64,
        success: bool,
        cache_hit: bool,
        cost_usd: f64,
        quality: Option<f64>,
        expected_total: f64,
    }
    let rows: Vec<Row> = serde_json::from_str(&fixture("rewards.json")).expect("decode rewards");
    assert!(!rows.is_empty());
    let policy = RewardPolicy::default();
    for r in rows.iter() {
        let mut o = Outcome::new(r.latency_ms, r.success, r.cost_usd);
        if r.cache_hit {
            o = o.cached();
        }
        if let Some(q) = r.quality {
            o = o.with_quality(q);
        }
        approx(policy.reward(&o), r.expected_total, 1e-12);
    }
}

/// Shared sampler moment vectors: statistical tolerance, fixed seeds, own
/// RNG. Never bitwise identity across languages by design.
#[test]
fn sampler_moments_match() {
    use rand::rngs::SmallRng;
    use rand::SeedableRng;
    #[derive(serde::Deserialize)]
    struct Vector {
        alpha: f64,
        beta: f64,
        n: usize,
        mean_lo: f64,
        mean_hi: f64,
    }
    #[derive(serde::Deserialize)]
    struct Doc {
        vectors: Vec<Vector>,
    }
    let doc: Doc = serde_json::from_str(&fixture("sampler.json")).expect("decode sampler");
    let sampler = thompson_sampling::Exact;
    for (i, v) in doc.vectors.iter().enumerate() {
        let mut rng = SmallRng::seed_from_u64(1000 + i as u64);
        let post = thompson_sampling::Posterior::new(v.alpha, v.beta).unwrap();
        let mut sum = 0.0;
        for _ in 0..v.n {
            sum += thompson_sampling::BetaSampler::sample(&sampler, &mut rng, &post);
        }
        let mean = sum / v.n as f64;
        assert!(
            mean >= v.mean_lo && mean <= v.mean_hi,
            "vector {i}: mean {mean} outside [{}, {}]",
            v.mean_lo,
            v.mean_hi
        );
    }
}
