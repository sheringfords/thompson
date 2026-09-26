//! The bandit policy: arm registry, selection strategy, and updates.

use crate::arm::{Arm, ArmStats};
use crate::discount::{DiscountPolicy, FixedDiscount};
use crate::error::{Error, Result};
use crate::observer::PolicyObserver;
use crate::posterior::{Posterior, UpdateRule};
use crate::reward::{Outcome, RewardPolicy};
use crate::sampler::{BetaSampler, Exact};
use crate::selection::{
    PhasedStrategy, SelectionStrategy, ThompsonStrategy, UcbRegularizedStrategy,
};
use crate::warm_start::{prior_for, InformedPrior, WarmStart};
use rand::RngCore;
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

/// How an arm is chosen from the current posteriors.
#[derive(Debug, Clone, Copy, PartialEq, Default, Serialize, Deserialize)]
#[serde(tag = "selection", rename_all = "snake_case")]
pub enum Selection {
    /// Draw once per arm and take the argmax. Plain Thompson Sampling.
    #[default]
    Thompson,

    /// Thompson Sampling with a UCB-style bonus added to under-explored arms.
    ///
    /// Belt-and-braces: Thompson Sampling already explores optimally in the
    /// Bayesian sense, so the bonus is redundant under an exact sampler and
    /// mostly serves to slow convergence. It earns its keep only when paired
    /// with an approximate sampler that under-explores, which is the usual
    /// reason it appears in production code.
    UcbRegularized {
        /// Bonus coefficient.
        c: f64,
        /// Arms with at least this many pulls stop receiving the bonus.
        until_pulls: u64,
    },

    /// Round-robin every arm to a fixed pull count, then sample as normal.
    ///
    /// Cold-start protection for settings where an unmeasured arm is genuinely
    /// dangerous rather than merely unknown — you want a measurement on every
    /// option before any of them carries real traffic. The cost is direct and
    /// large: until the quota is met, every arm gets equal traffic regardless
    /// of how bad it has already proven to be.
    ///
    /// The effective quota is `max(bootstrap, min_pulls_for_exploit)`.
    Phased {
        /// Pulls each arm receives before any exploitation happens.
        bootstrap: u64,
        /// Pulls required before an arm may be selected by sampling.
        min_pulls_for_exploit: u64,
    },
}

/// Policy configuration. Serialisable so a running bandit can be snapshotted.
#[derive(Debug, Clone, Copy, PartialEq, Default, Serialize, Deserialize)]
pub struct Config {
    /// How rewards are folded into the posterior.
    pub update_rule: UpdateRule,
    /// How outcomes are collapsed into a scalar reward.
    pub reward_policy: RewardPolicy,
    /// How new arms are initialised.
    pub warm_start: WarmStart,
    /// How arms are selected.
    pub selection: Selection,
    /// Per-round discount in `(0, 1]` applied to every posterior.
    ///
    /// `None` means stationary: evidence never expires. Set this when arm
    /// quality drifts — provider capacity changes, models are silently updated
    /// behind a stable name — otherwise early evidence pins the posterior and
    /// the bandit cannot notice.
    pub discount: Option<f64>,
}

/// A Thompson Sampling policy over a mutable set of arms.
///
/// Arms are held in a [`BTreeMap`], so iteration order is deterministic and a
/// run is reproducible from its seed. This matters more than it looks: with a
/// hash map, iteration order supplies incidental randomness that can mask a
/// sampler doing no exploration of its own.
pub struct ThompsonSampling {
    arms: BTreeMap<String, Arm>,
    config: Config,
    sampler: Box<dyn BetaSampler>,
    total_pulls: u64,
    observer: Option<Box<dyn PolicyObserver>>,
}

impl std::fmt::Debug for ThompsonSampling {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ThompsonSampling")
            .field("arms", &self.arms)
            .field("config", &self.config)
            .field("sampler", &self.sampler)
            .field("total_pulls", &self.total_pulls)
            .field("observer", &self.observer.as_ref().map(|_| "<observer>"))
            .finish()
    }
}

impl ThompsonSampling {
    /// Create an empty policy with the given configuration and sampler.
    pub fn new(config: Config, sampler: Box<dyn BetaSampler>) -> Self {
        ThompsonSampling {
            arms: BTreeMap::new(),
            config,
            sampler,
            total_pulls: 0,
            observer: None,
        }
    }

    /// Attach an observer for metrics/logging. Replaces any existing observer.
    pub fn set_observer(&mut self, observer: Box<dyn PolicyObserver>) {
        self.observer = Some(observer);
    }

    /// Builder-style observer attachment.
    pub fn with_observer(mut self, observer: Box<dyn PolicyObserver>) -> Self {
        self.observer = Some(observer);
        self
    }

    /// Remove any attached observer.
    pub fn clear_observer(&mut self) {
        self.observer = None;
    }

    /// Current discount as a first-class policy. Wraps `config.discount`.
    pub fn discount_policy(&self) -> FixedDiscount {
        FixedDiscount::new(self.config.discount)
    }

    /// Effective memory in observations implied by the current discount.
    pub fn effective_memory(&self) -> f64 {
        self.discount_policy().effective_memory()
    }

    /// Create a policy with default configuration and the exact sampler.
    pub fn with_defaults<I, S>(arm_ids: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        let mut policy = ThompsonSampling::new(Config::default(), Box::new(Exact));
        for id in arm_ids {
            policy.add_arm(id.into());
        }
        policy
    }

    /// Register an arm, choosing its prior via the configured warm-start
    /// strategy. Existing arms are left untouched.
    ///
    /// Returns the prior that was applied, or `None` if the arm already existed.
    pub fn add_arm(&mut self, id: String) -> Option<InformedPrior> {
        if self.arms.contains_key(&id) {
            return None;
        }
        let prior = prior_for(&self.config.warm_start, &id, self.arms.values());
        self.insert(id, prior);
        Some(prior)
    }

    /// Register an arm with an explicit prior, overriding the warm-start
    /// strategy. Existing arms are left untouched.
    pub fn add_arm_with_prior(&mut self, id: String, prior: InformedPrior) -> bool {
        if self.arms.contains_key(&id) {
            return false;
        }
        self.insert(id, prior);
        true
    }

    fn insert(&mut self, id: String, prior: InformedPrior) {
        let mut arm = Arm::new(id.clone(), prior.to_posterior());
        arm.warm_started = prior != InformedPrior::new(1.0, 1.0);
        let warm_started = arm.warm_started;
        self.arms.insert(id.clone(), arm);
        if let Some(obs) = &self.observer {
            obs.on_arm_added(&id, warm_started);
        }
    }

    /// Remove an arm, returning it if it was present.
    pub fn remove_arm(&mut self, id: &str) -> Option<Arm> {
        self.arms.remove(id)
    }

    /// Whether an arm is registered.
    pub fn has_arm(&self, id: &str) -> bool {
        self.arms.contains_key(id)
    }

    /// Look up an arm.
    pub fn arm(&self, id: &str) -> Option<&Arm> {
        self.arms.get(id)
    }

    /// Number of registered arms.
    pub fn len(&self) -> usize {
        self.arms.len()
    }

    /// Whether any arms are registered.
    pub fn is_empty(&self) -> bool {
        self.arms.is_empty()
    }

    /// Total observations recorded across all arms.
    pub fn total_pulls(&self) -> u64 {
        self.total_pulls
    }

    /// The active configuration.
    pub fn config(&self) -> &Config {
        &self.config
    }

    /// The active sampler's name.
    pub fn sampler_name(&self) -> &'static str {
        self.sampler.name()
    }

    /// Choose an arm.
    ///
    /// This does not mutate the policy: nothing is learned until the outcome
    /// comes back through [`record`](Self::record) or
    /// [`record_outcome`](Self::record_outcome). Selecting without recording is
    /// legitimate — a request may be cancelled — and simply teaches nothing.
    pub fn select(&self, rng: &mut dyn RngCore) -> Result<String> {
        if self.arms.is_empty() {
            return Err(Error::NoArms);
        }

        // When observer is present, capture the actual sampled scores that
        // produced the decision (no double RNG). When absent, take fast path
        // without allocation.
        let (chosen, sampled_scores) = match self.config.selection {
            Selection::Thompson => {
                if self.observer.is_some() {
                    self.argmax_sampled_with_scores(rng, |_| true)
                } else {
                    (self.argmax_sampled(rng, |_| true), Vec::new())
                }
            }
            Selection::UcbRegularized { c, until_pulls } => {
                // NaN poisons every score comparison (all false), silently
                // degenerating selection to first-arm-wins.
                if !c.is_finite() {
                    return Err(Error::InvalidParameter {
                        parameter: "selection.ucb_regularized.c".to_string(),
                        value: c,
                    });
                }
                if self.observer.is_some() {
                    self.argmax_ucb_with_scores(rng, c, until_pulls)
                } else {
                    (self.argmax_ucb(rng, c, until_pulls), Vec::new())
                }
            }
            Selection::Phased {
                bootstrap,
                min_pulls_for_exploit,
            } => {
                let forced = bootstrap.max(min_pulls_for_exploit);
                if let Some(id) = self.least_pulled_below(forced) {
                    // Phased forced choice is deterministic; scores are means for observability.
                    let scores = if self.observer.is_some() {
                        self.arms
                            .values()
                            .map(|a| (a.id.as_str(), a.posterior.mean()))
                            .collect()
                    } else {
                        Vec::new()
                    };
                    (id, scores)
                } else if self.observer.is_some() {
                    self.argmax_sampled_with_scores(rng, |_| true)
                } else {
                    (self.argmax_sampled(rng, |_| true), Vec::new())
                }
            }
        };

        if let Some(obs) = &self.observer {
            obs.on_select(&chosen, &sampled_scores);
        }

        Ok(chosen)
    }

    /// Choose an arm using a custom [`SelectionStrategy`], bypassing
    /// `config.selection`. This is the first-class extension point for
    /// selection: implement the trait out-of-tree and pass it here without
    /// forking `policy.rs`.
    pub fn select_with(
        &self,
        rng: &mut dyn RngCore,
        strategy: &dyn SelectionStrategy,
    ) -> Result<String> {
        if self.arms.is_empty() {
            return Err(Error::NoArms);
        }
        let chosen = strategy.select(rng, &self.arms, self.sampler.as_ref(), self.total_pulls);
        if let Some(obs) = &self.observer {
            let scores: Vec<(&str, f64)> = self
                .arms
                .values()
                .map(|a| (a.id.as_str(), a.posterior.mean()))
                .collect();
            obs.on_select(&chosen, &scores);
        }
        Ok(chosen)
    }

    /// Convenience: build a strategy from `config.selection` as a trait object.
    pub fn selection_strategy(&self) -> Box<dyn SelectionStrategy> {
        match self.config.selection {
            Selection::Thompson => Box::new(ThompsonStrategy),
            Selection::UcbRegularized { c, until_pulls } => {
                Box::new(UcbRegularizedStrategy { c, until_pulls })
            }
            Selection::Phased {
                bootstrap,
                min_pulls_for_exploit,
            } => Box::new(PhasedStrategy {
                bootstrap,
                min_pulls_for_exploit,
            }),
        }
    }

    /// Argmax of one posterior draw per arm, restricted to arms passing `filter`.
    fn argmax_sampled(&self, rng: &mut dyn RngCore, filter: impl Fn(&Arm) -> bool) -> String {
        let mut best: Option<(&str, f64)> = None;
        for arm in self.arms.values() {
            if !filter(arm) {
                continue;
            }
            let score = self.sampler.sample(rng, &arm.posterior);
            if best.map_or(true, |(_, b)| score > b) {
                best = Some((&arm.id, score));
            }
        }
        best.expect("filter matched at least one arm").0.to_string()
    }

    fn argmax_sampled_with_scores(
        &self,
        rng: &mut dyn RngCore,
        filter: impl Fn(&Arm) -> bool,
    ) -> (String, Vec<(&str, f64)>) {
        let mut best: Option<(&str, f64)> = None;
        let mut scores: Vec<(&str, f64)> = Vec::with_capacity(self.arms.len());
        for arm in self.arms.values() {
            if !filter(arm) {
                continue;
            }
            let score = self.sampler.sample(rng, &arm.posterior);
            scores.push((arm.id.as_str(), score));
            if best.map_or(true, |(_, b)| score > b) {
                best = Some((&arm.id, score));
            }
        }
        (
            best.expect("filter matched at least one arm").0.to_string(),
            scores,
        )
    }

    fn argmax_ucb(&self, rng: &mut dyn RngCore, c: f64, until_pulls: u64) -> String {
        // `ln(total_pulls)` is undefined at zero and negative at one, either of
        // which poisons every comparison with NaN. Shifting by one keeps the
        // bonus finite and non-negative from the very first round.
        let log_total = ((self.total_pulls + 1) as f64).ln();

        let mut best: Option<(&str, f64)> = None;
        for arm in self.arms.values() {
            let sample = self.sampler.sample(rng, &arm.posterior);
            let score = if arm.pulls() >= until_pulls {
                sample
            } else if arm.pulls() == 0 {
                f64::INFINITY
            } else {
                sample + c * (log_total / arm.pulls() as f64).sqrt()
            };

            if best.map_or(true, |(_, b)| score > b) {
                best = Some((&arm.id, score));
            }
        }
        best.expect("arm set is non-empty").0.to_string()
    }

    fn argmax_ucb_with_scores(
        &self,
        rng: &mut dyn RngCore,
        c: f64,
        until_pulls: u64,
    ) -> (String, Vec<(&str, f64)>) {
        let log_total = ((self.total_pulls + 1) as f64).ln();
        let mut best: Option<(&str, f64)> = None;
        let mut scores: Vec<(&str, f64)> = Vec::with_capacity(self.arms.len());
        for arm in self.arms.values() {
            let sample = self.sampler.sample(rng, &arm.posterior);
            let score = if arm.pulls() >= until_pulls {
                sample
            } else if arm.pulls() == 0 {
                f64::INFINITY
            } else {
                sample + c * (log_total / arm.pulls() as f64).sqrt()
            };
            scores.push((arm.id.as_str(), score));
            if best.map_or(true, |(_, b)| score > b) {
                best = Some((&arm.id, score));
            }
        }
        (best.expect("arm set is non-empty").0.to_string(), scores)
    }

    /// The least-pulled arm, if any arm has fewer than `threshold` pulls.
    fn least_pulled_below(&self, threshold: u64) -> Option<String> {
        self.arms
            .values()
            .filter(|arm| arm.pulls() < threshold)
            .min_by_key(|arm| arm.pulls())
            .map(|arm| arm.id.clone())
    }

    /// Record a raw reward in `[0, 1]` against an arm.
    pub fn record(&mut self, rng: &mut dyn RngCore, id: &str, reward: f64) -> Result<()> {
        let rule = self.config.update_rule;

        // Borrow observer first to avoid borrow conflict with arms.
        let discount = FixedDiscount::new(self.config.discount);

        let posterior_snapshot: Posterior;
        {
            let arm = self
                .arms
                .get_mut(id)
                .ok_or_else(|| Error::UnknownArm { id: id.to_string() })?;
            arm.posterior.observe(rng, reward, rule)?;
            arm.cumulative_reward += reward;
            posterior_snapshot = arm.posterior;
        }

        self.total_pulls += 1;

        if let Some(factor) = discount.factor() {
            // Discount every arm, not only the one played: the point is that
            // evidence ages, and an arm that has not been tried lately has the
            // stalest evidence of all.
            for arm in self.arms.values_mut() {
                discount.apply(&mut arm.posterior);
            }
            if let Some(obs) = &self.observer {
                obs.on_discount(factor);
            }
        }

        if let Some(obs) = &self.observer {
            obs.on_record(id, reward, &posterior_snapshot);
        }

        Ok(())
    }

    /// Score an outcome through the reward policy and record it.
    pub fn record_outcome(
        &mut self,
        rng: &mut dyn RngCore,
        id: &str,
        outcome: &Outcome,
    ) -> Result<()> {
        self.config.reward_policy.weights.validate()?;
        let reward = self.config.reward_policy.reward(outcome);
        self.record(rng, id, reward)
    }

    /// Per-arm summaries, best posterior mean first.
    pub fn stats(&self) -> Vec<ArmStats> {
        let mut stats: Vec<ArmStats> = self.arms.values().map(ArmStats::from).collect();
        stats.sort_by(|a, b| {
            b.posterior_mean
                .partial_cmp(&a.posterior_mean)
                .unwrap_or(std::cmp::Ordering::Equal)
                .then_with(|| a.id.cmp(&b.id))
        });
        stats
    }

    /// The arm with the highest posterior mean among those with at least
    /// `min_pulls` observations.
    pub fn best_arm(&self, min_pulls: u64) -> Option<&str> {
        self.arms
            .values()
            .filter(|arm| arm.pulls() >= min_pulls)
            .max_by(|a, b| {
                a.posterior
                    .mean()
                    .partial_cmp(&b.posterior.mean())
                    .unwrap_or(std::cmp::Ordering::Equal)
            })
            .map(|arm| arm.id.as_str())
    }

    /// Capture the full learned state.
    pub fn snapshot(&self) -> Snapshot {
        Snapshot {
            version: Snapshot::VERSION,
            config: self.config,
            arms: self.arms.values().cloned().collect(),
            total_pulls: self.total_pulls,
        }
    }

    /// Rebuild a policy from a snapshot.
    ///
    /// The sampler is not part of the snapshot: it is a strategy, not state,
    /// and restoring under a different sampler is a legitimate experiment.
    pub fn restore(snapshot: Snapshot, sampler: Box<dyn BetaSampler>) -> Result<Self> {
        if snapshot.version != Snapshot::VERSION {
            return Err(Error::Decode(format!(
                "unsupported snapshot version {} (expected {})",
                snapshot.version,
                Snapshot::VERSION
            )));
        }

        let mut arms = BTreeMap::new();
        for arm in snapshot.arms {
            Posterior::new(arm.posterior.alpha, arm.posterior.beta)?;
            // A snapshot is deserialised input, so it can name the same arm
            // twice. Inserting silently would drop one of the two histories;
            // rejecting says which arm was ambiguous.
            if arms.contains_key(&arm.id) {
                return Err(Error::Decode(format!("duplicate arm id: {}", arm.id)));
            }
            arms.insert(arm.id.clone(), arm);
        }

        Ok(ThompsonSampling {
            arms,
            config: snapshot.config,
            sampler,
            total_pulls: snapshot.total_pulls,
            observer: None,
        })
    }

    /// Persist via a [`SnapshotStore`](crate::persistence::SnapshotStore).
    pub fn save_to_store(
        &self,
        store: &dyn crate::persistence::SnapshotStore,
    ) -> crate::error::Result<()> {
        store.save(&self.snapshot())
    }

    /// Restore via a store, with an explicit sampler.
    pub fn restore_from_store(
        store: &dyn crate::persistence::SnapshotStore,
        sampler: Box<dyn BetaSampler>,
    ) -> crate::error::Result<Option<Self>> {
        match store.load()? {
            None => Ok(None),
            Some(snapshot) => Ok(Some(Self::restore(snapshot, sampler)?)),
        }
    }
}

/// A serialisable capture of a policy's learned state.
///
/// # JSON is not bit-exact
///
/// A round trip through [`to_json`](Self::to_json) and
/// [`from_json`](Self::from_json) can shift a float by one unit in the last
/// place: `serde_json`'s parser is not correctly rounded on every input. The
/// error is ~1e-16 relative and does not accumulate across successive round
/// trips of the same value, so it is irrelevant to selection — but it does mean
/// a snapshot is not a byte-identical fingerprint of a policy. Compare restored
/// policies with a tolerance, not with `==`.
///
/// # Legacy readers
///
/// PascalCase aliases (`Arms`, `ID`, `Alpha`, …) decode pre-canonical Go
/// snapshots; writers always emit canonical snake_case.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Snapshot {
    /// Snapshot format version.
    pub version: u32,
    /// Configuration in force when the snapshot was taken.
    ///
    /// `default` keeps pre-canonical Go snapshots (which omitted `config`)
    /// readable: they restore under the default configuration. New writers
    /// always include it.
    #[serde(default)]
    pub config: Config,
    /// Every arm and its posterior.
    #[serde(alias = "Arms")]
    pub arms: Vec<Arm>,
    /// Total observations recorded.
    #[serde(alias = "TotalPulls")]
    pub total_pulls: u64,
}

impl Snapshot {
    /// Current snapshot format version.
    pub const VERSION: u32 = 1;

    /// Encode as pretty-printed JSON.
    pub fn to_json(&self) -> Result<String> {
        serde_json::to_string_pretty(self).map_err(|e| Error::Decode(e.to_string()))
    }

    /// Decode from JSON.
    pub fn from_json(json: &str) -> Result<Self> {
        serde_json::from_str(json).map_err(|e| Error::Decode(e.to_string()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::sampler::legacy::Deterministic;
    use rand::rngs::SmallRng;
    use rand::SeedableRng;
    use std::collections::BTreeMap;

    fn rng() -> SmallRng {
        SmallRng::seed_from_u64(0xBEEF)
    }

    fn counts(
        policy: &ThompsonSampling,
        rounds: usize,
        rng: &mut SmallRng,
    ) -> BTreeMap<String, u32> {
        let mut counts = BTreeMap::new();
        for _ in 0..rounds {
            let id = policy.select(rng).unwrap();
            *counts.entry(id).or_insert(0) += 1;
        }
        counts
    }

    #[test]
    fn empty_policy_cannot_select() {
        let policy = ThompsonSampling::new(Config::default(), Box::new(Exact));
        assert_eq!(policy.select(&mut rng()).unwrap_err(), Error::NoArms);
    }

    #[test]
    fn recording_an_unknown_arm_is_an_error() {
        let mut policy = ThompsonSampling::with_defaults(["a"]);
        let err = policy.record(&mut rng(), "nope", 1.0).unwrap_err();
        assert_eq!(
            err,
            Error::UnknownArm {
                id: "nope".to_string()
            }
        );
        assert_eq!(policy.total_pulls(), 0);
    }

    #[test]
    fn record_outcome_survives_unscorable_measurements() {
        // A judge that returns NaN and a request that times out are both things
        // a caller will hand over eventually. Neither is a malformed reward, so
        // neither should come back as `RewardOutOfRange` — an error naming a
        // reward the caller never supplied.
        let mut policy = ThompsonSampling::with_defaults(["a"]);
        let mut rng = rng();

        let unscored = Outcome::new(320.0, true, 0.0012).with_quality(f64::NAN);
        policy.record_outcome(&mut rng, "a", &unscored).unwrap();

        let timed_out = Outcome::new(f64::INFINITY, true, f64::INFINITY);
        policy.record_outcome(&mut rng, "a", &timed_out).unwrap();

        assert_eq!(policy.arm("a").unwrap().pulls(), 2);
    }

    #[test]
    fn converges_on_the_better_arm() {
        let mut policy = ThompsonSampling::with_defaults(["good", "bad"]);
        let mut rng = rng();

        for _ in 0..400 {
            let id = policy.select(&mut rng).unwrap();
            let reward = if id == "good" { 0.9 } else { 0.1 };
            policy.record(&mut rng, &id, reward).unwrap();
        }

        assert_eq!(policy.best_arm(1), Some("good"));
        let good = policy.arm("good").unwrap();
        let bad = policy.arm("bad").unwrap();
        assert!(
            good.pulls() > bad.pulls() * 3,
            "good={} bad={}",
            good.pulls(),
            bad.pulls()
        );
    }

    #[test]
    fn exact_sampler_keeps_exploring_a_dominated_arm() {
        // Thompson Sampling must not close off an arm entirely; a run that
        // never revisits the loser cannot recover from a bad early streak.
        let mut policy = ThompsonSampling::with_defaults(["good", "bad"]);
        let mut rng = rng();
        for _ in 0..300 {
            let id = policy.select(&mut rng).unwrap();
            let reward = if id == "good" { 0.9 } else { 0.2 };
            policy.record(&mut rng, &id, reward).unwrap();
        }
        assert!(policy.arm("bad").unwrap().pulls() > 0);
    }

    #[test]
    fn deterministic_sampler_performs_no_exploration() {
        // The defect that motivated this crate: with a deterministic sampler,
        // selection is a fixed function of state, so a single lucky first
        // outcome can lock the policy onto one arm permanently.
        let mut policy = ThompsonSampling::new(
            Config {
                update_rule: UpdateRule::Binarize { threshold: 0.5 },
                ..Config::default()
            },
            Box::new(Deterministic),
        );
        policy.add_arm_with_prior("a".into(), InformedPrior::new(1.0, 1.0));
        policy.add_arm_with_prior("b".into(), InformedPrior::new(1.0, 1.0));

        let mut rng = rng();
        // Give "a" one success, which is enough to raise its mean above "b".
        policy.record(&mut rng, "a", 1.0).unwrap();

        let selections = counts(&policy, 200, &mut rng);
        assert_eq!(
            selections.get("a"),
            Some(&200),
            "expected total lock-on, got {selections:?}"
        );
        assert_eq!(selections.get("b"), None);
    }

    #[test]
    fn ucb_regularization_survives_the_first_round() {
        // `ln(total_pulls)` at zero pulls is the classic NaN trap: every
        // comparison against NaN is false, so selection silently degenerates.
        let mut policy = ThompsonSampling::new(
            Config {
                selection: Selection::UcbRegularized {
                    c: 2.0,
                    until_pulls: 30,
                },
                ..Config::default()
            },
            Box::new(Exact),
        );
        for id in ["a", "b", "c"] {
            policy.add_arm(id.into());
        }

        let mut rng = rng();
        assert_eq!(policy.total_pulls(), 0);
        let first = policy.select(&mut rng).unwrap();
        assert!(["a", "b", "c"].contains(&first.as_str()));
    }

    #[test]
    fn ucb_regularization_visits_every_arm_early() {
        let mut policy = ThompsonSampling::new(
            Config {
                selection: Selection::UcbRegularized {
                    c: 2.0,
                    until_pulls: 30,
                },
                ..Config::default()
            },
            Box::new(Exact),
        );
        for id in ["a", "b", "c"] {
            policy.add_arm(id.into());
        }

        let mut rng = rng();
        for _ in 0..60 {
            let id = policy.select(&mut rng).unwrap();
            let reward = if id == "a" { 0.9 } else { 0.1 };
            policy.record(&mut rng, &id, reward).unwrap();
        }
        for id in ["a", "b", "c"] {
            assert!(policy.arm(id).unwrap().pulls() > 0, "{id} was never pulled");
        }
    }

    #[test]
    fn phased_selection_bootstraps_every_arm_first() {
        let mut policy = ThompsonSampling::new(
            Config {
                selection: Selection::Phased {
                    bootstrap: 5,
                    min_pulls_for_exploit: 10,
                },
                ..Config::default()
            },
            Box::new(Exact),
        );
        for id in ["a", "b", "c"] {
            policy.add_arm(id.into());
        }

        let mut rng = rng();
        for _ in 0..15 {
            let id = policy.select(&mut rng).unwrap();
            policy.record(&mut rng, &id, 0.5).unwrap();
        }

        // 15 rounds over 3 arms with a bootstrap of 5 means exactly even.
        for id in ["a", "b", "c"] {
            assert_eq!(policy.arm(id).unwrap().pulls(), 5, "{id}");
        }
    }

    #[test]
    fn phased_selection_does_not_stall_below_the_exploit_threshold() {
        let mut policy = ThompsonSampling::new(
            Config {
                selection: Selection::Phased {
                    bootstrap: 2,
                    min_pulls_for_exploit: 1_000,
                },
                ..Config::default()
            },
            Box::new(Exact),
        );
        for id in ["a", "b"] {
            policy.add_arm(id.into());
        }

        // No arm can ever reach 1000 pulls within this loop, so the fail-safe
        // path is the only thing keeping selection alive.
        let mut rng = rng();
        for _ in 0..50 {
            let id = policy.select(&mut rng).unwrap();
            policy.record(&mut rng, &id, 0.5).unwrap();
        }
        assert_eq!(policy.total_pulls(), 50);
        assert_eq!(policy.arm("a").unwrap().pulls(), 25);
        assert_eq!(policy.arm("b").unwrap().pulls(), 25);
    }

    #[test]
    fn phased_selection_exploits_once_the_quota_is_met() {
        // Regression guard. Gating exploitation on a per-arm threshold while
        // exploiting only among arms past it locks the policy onto the first
        // arm to cross, permanently. Here the quota is 10 per arm, so from
        // round 20 onward the good arm must start pulling ahead.
        let mut policy = ThompsonSampling::new(
            Config {
                selection: Selection::Phased {
                    bootstrap: 10,
                    min_pulls_for_exploit: 10,
                },
                ..Config::default()
            },
            Box::new(Exact),
        );
        for id in ["bad", "good"] {
            policy.add_arm_with_prior(id.into(), InformedPrior::new(1.0, 1.0));
        }

        let mut rng = rng();
        for _ in 0..600 {
            let id = policy.select(&mut rng).unwrap();
            let reward = if id == "good" { 0.95 } else { 0.05 };
            policy.record(&mut rng, &id, reward).unwrap();
        }

        let good = policy.arm("good").unwrap().pulls();
        let bad = policy.arm("bad").unwrap().pulls();
        assert!(bad >= 10, "quota not honoured: bad has {bad} pulls");
        assert!(
            good > bad * 4,
            "expected exploitation after the quota: good={good} bad={bad}"
        );
    }

    #[test]
    fn adding_an_arm_twice_preserves_its_history() {
        let mut policy = ThompsonSampling::with_defaults(["a"]);
        let mut rng = rng();
        for _ in 0..20 {
            policy.record(&mut rng, "a", 0.9).unwrap();
        }
        let before = policy.arm("a").unwrap().clone();

        assert!(policy.add_arm("a".into()).is_none());
        assert!(!policy.add_arm_with_prior("a".into(), InformedPrior::new(1.0, 1.0)));
        assert_eq!(policy.arm("a").unwrap(), &before);
    }

    #[test]
    fn warm_started_arm_is_flagged_and_cold_arm_is_not() {
        let mut policy = ThompsonSampling::with_defaults(Vec::<String>::new());
        let mut rng = rng();

        policy.add_arm("openai/gpt-4".into());
        // First arm has no neighbours, so it takes the fallback prior.
        for _ in 0..50 {
            policy.record(&mut rng, "openai/gpt-4", 0.95).unwrap();
        }

        policy.add_arm("openai/gpt-4.5-turbo".into());
        let fresh = policy.arm("openai/gpt-4.5-turbo").unwrap();
        assert!(fresh.warm_started);
        assert_eq!(fresh.pulls(), 0);
        assert!(
            fresh.posterior.mean() > 0.5,
            "expected to inherit the neighbour's optimism, got {}",
            fresh.posterior.mean()
        );
    }

    #[test]
    fn discounting_lets_the_policy_follow_a_regime_change() {
        let mut policy = ThompsonSampling::new(
            Config {
                discount: Some(0.99),
                ..Config::default()
            },
            Box::new(Exact),
        );
        for id in ["a", "b"] {
            policy.add_arm(id.into());
        }

        let mut rng = rng();
        // Phase 1: "a" is the good arm.
        for _ in 0..500 {
            let id = policy.select(&mut rng).unwrap();
            let reward = if id == "a" { 0.9 } else { 0.1 };
            policy.record(&mut rng, &id, reward).unwrap();
        }
        assert_eq!(policy.best_arm(1), Some("a"));

        // Phase 2: the arms swap quality.
        for _ in 0..1_500 {
            let id = policy.select(&mut rng).unwrap();
            let reward = if id == "b" { 0.9 } else { 0.1 };
            policy.record(&mut rng, &id, reward).unwrap();
        }
        assert_eq!(
            policy.best_arm(1),
            Some("b"),
            "discounted policy should have followed the switch: {:?}",
            policy.stats()
        );
    }

    #[test]
    fn snapshot_round_trips_through_json() {
        let mut policy = ThompsonSampling::with_defaults(["a", "b"]);
        let mut rng = rng();
        for _ in 0..30 {
            let id = policy.select(&mut rng).unwrap();
            policy.record(&mut rng, &id, 0.8).unwrap();
        }

        let json = policy.snapshot().to_json().unwrap();
        let restored =
            ThompsonSampling::restore(Snapshot::from_json(&json).unwrap(), Box::new(Exact))
                .unwrap();

        assert_eq!(restored.total_pulls(), policy.total_pulls());

        let (before, after) = (policy.stats(), restored.stats());
        assert_eq!(before.len(), after.len());
        for (b, a) in before.iter().zip(after.iter()) {
            assert_eq!(a.id, b.id);
            assert_eq!(a.pulls, b.pulls);
            assert_eq!(a.warm_started, b.warm_started);
            // Tolerance, not equality: see the note on `Snapshot`.
            assert!((a.alpha - b.alpha).abs() < 1e-9);
            assert!((a.beta - b.beta).abs() < 1e-9);
            assert!((a.posterior_mean - b.posterior_mean).abs() < 1e-9);
        }
    }

    #[test]
    fn json_round_trip_is_lossy_at_the_last_bit() {
        // Documents a limitation rather than asserting a guarantee: serde_json
        // does not reparse every f64 to the identical bit pattern. Recorded so
        // nobody builds a checksum or an equality assertion on top of a
        // snapshot and is surprised in production.
        let mut policy = ThompsonSampling::new(
            Config {
                update_rule: UpdateRule::Fractional,
                ..Config::default()
            },
            Box::new(Exact),
        );
        policy.add_arm_with_prior("a".into(), InformedPrior::new(1.0, 1.0));

        let mut rng = rng();
        for _ in 0..14 {
            policy.record(&mut rng, "a", 0.8).unwrap();
        }

        let snapshot = policy.snapshot();
        let reparsed = Snapshot::from_json(&snapshot.to_json().unwrap()).unwrap();

        let original = snapshot.arms[0].cumulative_reward;
        let restored = reparsed.arms[0].cumulative_reward;
        assert!(
            (original - restored).abs() < 1e-12,
            "drift must stay at the ULP scale: {original} vs {restored}"
        );
    }

    #[test]
    fn restore_rejects_an_unknown_version() {
        let policy = ThompsonSampling::with_defaults(["a"]);
        let mut snapshot = policy.snapshot();
        snapshot.version = 999;
        assert!(ThompsonSampling::restore(snapshot, Box::new(Exact)).is_err());
    }

    #[test]
    fn restore_rejects_a_corrupt_posterior() {
        let policy = ThompsonSampling::with_defaults(["a"]);
        let mut snapshot = policy.snapshot();
        snapshot.arms[0].posterior.alpha = 0.0;
        assert!(ThompsonSampling::restore(snapshot, Box::new(Exact)).is_err());
    }

    #[test]
    fn restore_rejects_a_duplicated_arm() {
        let policy = ThompsonSampling::with_defaults(["a"]);
        let mut snapshot = policy.snapshot();
        let duplicate = snapshot.arms[0].clone();
        snapshot.arms.push(duplicate);
        assert!(ThompsonSampling::restore(snapshot, Box::new(Exact)).is_err());
    }

    #[test]
    fn selection_is_reproducible_from_a_seed() {
        let run = || {
            let mut policy = ThompsonSampling::with_defaults(["a", "b", "c"]);
            let mut rng = SmallRng::seed_from_u64(1234);
            let mut picks = Vec::new();
            for _ in 0..200 {
                let id = policy.select(&mut rng).unwrap();
                policy.record(&mut rng, &id, 0.5).unwrap();
                picks.push(id);
            }
            picks
        };
        assert_eq!(run(), run());
    }

    #[test]
    fn removing_an_arm_takes_it_out_of_selection() {
        let mut policy = ThompsonSampling::with_defaults(["a", "b"]);
        assert!(policy.remove_arm("a").is_some());
        assert!(policy.remove_arm("a").is_none());
        assert_eq!(policy.len(), 1);

        let mut rng = rng();
        for _ in 0..50 {
            assert_eq!(policy.select(&mut rng).unwrap(), "b");
        }
    }

    #[test]
    fn nan_ucb_coefficient_is_rejected() {
        // NaN poisons every score comparison (all false), silently
        // degenerating selection to first-arm-wins (matches Go port).
        let mut policy = ThompsonSampling::new(
            Config {
                selection: Selection::UcbRegularized {
                    c: f64::NAN,
                    until_pulls: 30,
                },
                ..Config::default()
            },
            Box::new(crate::sampler::Exact),
        );
        for id in ["a", "b"] {
            policy.add_arm(id.into());
        }
        assert!(policy.select(&mut rng()).is_err());
    }
}
