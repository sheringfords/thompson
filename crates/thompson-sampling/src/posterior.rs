//! The Beta posterior over an arm's success probability.

use crate::error::{Error, Result};
use rand::Rng;
use serde::{Deserialize, Serialize};

/// How a reward in `[0, 1]` is folded into a Beta posterior.
///
/// The choice matters. `Binarize` is what most production routers do, and it is
/// the only rule that discards information: a reward of 0.61 and a reward of
/// 0.99 become the same observation. It also has no regret guarantee for
/// non-Bernoulli rewards. `Bernoulli` is the rule from Agrawal & Goyal (2012),
/// which extends the Beta-Bernoulli regret bound to arbitrary `[0, 1]` rewards
/// by first flipping a coin weighted by the reward. `Fractional` is a
/// pseudo-count update that is cheap and low-variance but is not a Bayesian
/// posterior update for any standard likelihood.
#[derive(Debug, Clone, Copy, PartialEq, Default, Serialize, Deserialize)]
#[serde(tag = "rule", rename_all = "snake_case")]
pub enum UpdateRule {
    /// `reward > threshold` counts as one success, otherwise one failure.
    Binarize {
        /// Rewards strictly above this value count as a success.
        threshold: f64,
    },
    /// Draw `u ~ U(0, 1)`; count a success when `u < reward`.
    #[default]
    Bernoulli,
    /// Add `reward` to alpha and `1 - reward` to beta.
    Fractional,
}

/// A `Beta(alpha, beta)` posterior plus the observation count that produced it.
///
/// `pulls` is tracked separately from `alpha + beta` because warm-started arms
/// begin with a concentrated posterior and zero observations, and several
/// selection strategies key their exploration bonus off real observations
/// rather than off prior mass.
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct Posterior {
    /// Beta shape parameter tracking successes.
    #[serde(alias = "Alpha")]
    pub alpha: f64,
    /// Beta shape parameter tracking failures.
    #[serde(alias = "Beta")]
    pub beta: f64,
    /// Number of real observations folded in since construction.
    #[serde(alias = "Pulls")]
    pub pulls: u64,
}

impl Default for Posterior {
    fn default() -> Self {
        Posterior::uninformative()
    }
}

impl Posterior {
    /// The uniform prior, `Beta(1, 1)`.
    pub const fn uninformative() -> Self {
        Posterior {
            alpha: 1.0,
            beta: 1.0,
            pulls: 0,
        }
    }

    /// Build a posterior, rejecting non-finite or non-positive parameters.
    pub fn new(alpha: f64, beta: f64) -> Result<Self> {
        if !alpha.is_finite() || !beta.is_finite() || alpha <= 0.0 || beta <= 0.0 {
            return Err(Error::InvalidBetaParams { alpha, beta });
        }
        Ok(Posterior {
            alpha,
            beta,
            pulls: 0,
        })
    }

    /// Posterior mean, `alpha / (alpha + beta)`.
    pub fn mean(&self) -> f64 {
        self.alpha / self.concentration()
    }

    /// Total prior + observed mass, `alpha + beta`.
    pub fn concentration(&self) -> f64 {
        self.alpha + self.beta
    }

    /// Posterior variance.
    pub fn variance(&self) -> f64 {
        let n = self.concentration();
        (self.alpha * self.beta) / (n * n * (n + 1.0))
    }

    /// Posterior standard deviation.
    pub fn std_dev(&self) -> f64 {
        self.variance().sqrt()
    }

    /// Width of an approximate 95% credible interval (normal approximation).
    ///
    /// Only meaningful once the posterior is reasonably concentrated; for a
    /// near-uniform posterior the Beta is not close to Gaussian.
    pub fn credible_width(&self) -> f64 {
        1.96 * self.std_dev()
    }

    /// Fold in one observation under `rule`.
    ///
    /// `reward` must lie in `[0, 1]`. `rng` is only consulted by
    /// [`UpdateRule::Bernoulli`].
    pub fn observe<R: Rng + ?Sized>(
        &mut self,
        rng: &mut R,
        reward: f64,
        rule: UpdateRule,
    ) -> Result<()> {
        if !reward.is_finite() || !(0.0..=1.0).contains(&reward) {
            return Err(Error::RewardOutOfRange { reward });
        }

        match rule {
            UpdateRule::Binarize { threshold } => {
                // Matches the Go port: [0, 1). NaN makes `reward > threshold`
                // always false (every observation a failure); threshold >= 1
                // binarizes everything to failure. Both fail loudly.
                if !threshold.is_finite() || !(0.0..1.0).contains(&threshold) {
                    // NaN makes `reward > threshold` always false (every
                    // observation a failure); out-of-range thresholds binarize
                    // everything one way. Both fail loudly.
                    return Err(Error::InvalidParameter {
                        parameter: "binarize.threshold".to_string(),
                        value: threshold,
                    });
                }
                if reward > threshold {
                    self.alpha += 1.0;
                } else {
                    self.beta += 1.0;
                }
            }
            UpdateRule::Bernoulli => {
                if rng.gen::<f64>() < reward {
                    self.alpha += 1.0;
                } else {
                    self.beta += 1.0;
                }
            }
            UpdateRule::Fractional => {
                self.alpha += reward;
                self.beta += 1.0 - reward;
            }
        }

        self.pulls += 1;
        Ok(())
    }

    /// Scale the posterior toward the uniform prior by `factor` in `(0, 1]`.
    ///
    /// This is the standard discounting trick for non-stationary environments:
    /// applying it every round gives the posterior an effective memory of
    /// roughly `1 / (1 - factor)` observations, so an arm that degrades is
    /// unlearned instead of being pinned by thousands of stale successes.
    pub fn discount(&mut self, factor: f64) {
        debug_assert!(factor > 0.0 && factor <= 1.0);
        self.alpha = 1.0 + (self.alpha - 1.0) * factor;
        self.beta = 1.0 + (self.beta - 1.0) * factor;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use rand::rngs::SmallRng;
    use rand::SeedableRng;

    #[test]
    fn rejects_non_positive_params() {
        assert!(Posterior::new(0.0, 1.0).is_err());
        assert!(Posterior::new(1.0, -1.0).is_err());
        assert!(Posterior::new(f64::NAN, 1.0).is_err());
        assert!(Posterior::new(f64::INFINITY, 1.0).is_err());
    }

    #[test]
    fn observe_rejects_nonfinite_binarize_threshold() {
        // A NaN threshold makes `reward > threshold` always false: every
        // observation silently becomes a failure. Out-of-range thresholds
        // binarize everything one way. Both fail loudly (matches Go port).
        let mut rng = SmallRng::seed_from_u64(1);
        let mut p = Posterior::uninformative();
        assert!(p
            .observe(
                &mut rng,
                0.7,
                UpdateRule::Binarize {
                    threshold: f64::NAN
                }
            )
            .is_err());
        assert!(p
            .observe(&mut rng, 0.7, UpdateRule::Binarize { threshold: 1.5 })
            .is_err());
        assert!(p
            .observe(&mut rng, 0.7, UpdateRule::Binarize { threshold: -0.5 })
            .is_err());
        assert!(p
            .observe(&mut rng, 0.7, UpdateRule::Binarize { threshold: 0.0 })
            .is_ok());
        assert!(p
            .observe(&mut rng, 0.7, UpdateRule::Binarize { threshold: 0.5 })
            .is_ok());
    }

    #[test]
    fn mean_and_variance_match_closed_form() {
        let p = Posterior::new(2.0, 3.0).unwrap();
        assert!((p.mean() - 0.4).abs() < 1e-12);
        // alpha*beta / ((a+b)^2 (a+b+1)) = 6 / (25 * 6) = 0.04
        assert!((p.variance() - 0.04).abs() < 1e-12);
    }

    #[test]
    fn binarize_ignores_reward_magnitude() {
        let mut rng = SmallRng::seed_from_u64(1);
        let rule = UpdateRule::Binarize { threshold: 0.6 };

        let mut low = Posterior::uninformative();
        let mut high = Posterior::uninformative();
        low.observe(&mut rng, 0.61, rule).unwrap();
        high.observe(&mut rng, 0.99, rule).unwrap();

        // This equality is the information loss, asserted so it stays visible.
        assert_eq!(low, high);
    }

    #[test]
    fn fractional_preserves_reward_magnitude() {
        let mut rng = SmallRng::seed_from_u64(1);
        let mut p = Posterior::uninformative();
        p.observe(&mut rng, 0.75, UpdateRule::Fractional).unwrap();
        assert!((p.alpha - 1.75).abs() < 1e-12);
        assert!((p.beta - 1.25).abs() < 1e-12);
        assert_eq!(p.pulls, 1);
    }

    #[test]
    fn bernoulli_converges_to_reward_rate() {
        let mut rng = SmallRng::seed_from_u64(42);
        let mut p = Posterior::uninformative();
        for _ in 0..20_000 {
            p.observe(&mut rng, 0.3, UpdateRule::Bernoulli).unwrap();
        }
        assert!((p.mean() - 0.3).abs() < 0.02, "mean was {}", p.mean());
    }

    #[test]
    fn rejects_out_of_range_reward() {
        let mut rng = SmallRng::seed_from_u64(1);
        let mut p = Posterior::uninformative();
        assert!(p.observe(&mut rng, 1.5, UpdateRule::Fractional).is_err());
        assert!(p.observe(&mut rng, -0.1, UpdateRule::Fractional).is_err());
        assert_eq!(p.pulls, 0);
    }

    #[test]
    fn discount_pulls_toward_uniform_prior() {
        let mut p = Posterior::new(101.0, 11.0).unwrap();
        p.discount(0.5);
        assert!((p.alpha - 51.0).abs() < 1e-12);
        assert!((p.beta - 6.0).abs() < 1e-12);

        // Repeated discounting must not push parameters below the prior.
        for _ in 0..1000 {
            p.discount(0.5);
        }
        assert!(p.alpha >= 1.0 && p.beta >= 1.0);
    }
}
