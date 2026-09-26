//! A single arm: its posterior and its observation history.

use crate::posterior::Posterior;
use serde::{Deserialize, Serialize};

/// One selectable option, together with everything learned about it.
///
/// PascalCase aliases decode pre-canonical Go snapshots; writers always
/// emit canonical snake_case.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Arm {
    /// Stable identifier, conventionally `provider/model`.
    #[serde(alias = "ID")]
    pub id: String,
    /// Current Beta posterior over this arm's success probability.
    #[serde(alias = "Posterior")]
    pub posterior: Posterior,
    /// Sum of raw rewards observed, before the update rule discretises them.
    #[serde(alias = "CumulativeReward")]
    pub cumulative_reward: f64,
    /// Whether this arm began from an informed prior rather than `Beta(1, 1)`.
    #[serde(alias = "WarmStarted")]
    pub warm_started: bool,
}

impl Arm {
    /// Create an arm with the given starting posterior.
    pub fn new(id: String, posterior: Posterior) -> Self {
        Arm {
            id,
            posterior,
            cumulative_reward: 0.0,
            warm_started: false,
        }
    }

    /// Number of observations recorded against this arm.
    pub fn pulls(&self) -> u64 {
        self.posterior.pulls
    }

    /// Mean of the raw rewards observed, or `None` before the first pull.
    ///
    /// This differs from the posterior mean: it is the untransformed average,
    /// unaffected by the prior and by the update rule's discretisation. Compare
    /// the two to see how much the reward collapse is distorting the estimate.
    pub fn empirical_mean(&self) -> Option<f64> {
        if self.posterior.pulls == 0 {
            None
        } else {
            Some(self.cumulative_reward / self.posterior.pulls as f64)
        }
    }
}

/// A read-only summary of an arm, ordered for reporting.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ArmStats {
    /// Arm identifier.
    pub id: String,
    /// Beta alpha parameter.
    pub alpha: f64,
    /// Beta beta parameter.
    pub beta: f64,
    /// Observations recorded.
    pub pulls: u64,
    /// Posterior mean.
    pub posterior_mean: f64,
    /// Mean of raw observed rewards, if any.
    pub empirical_mean: Option<f64>,
    /// Approximate 95% credible interval width.
    pub credible_width: f64,
    /// Whether the arm started from an informed prior.
    pub warm_started: bool,
}

impl From<&Arm> for ArmStats {
    fn from(arm: &Arm) -> Self {
        ArmStats {
            id: arm.id.clone(),
            alpha: arm.posterior.alpha,
            beta: arm.posterior.beta,
            pulls: arm.posterior.pulls,
            posterior_mean: arm.posterior.mean(),
            empirical_mean: arm.empirical_mean(),
            credible_width: arm.posterior.credible_width(),
            warm_started: arm.warm_started,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::posterior::UpdateRule;
    use rand::rngs::SmallRng;
    use rand::SeedableRng;

    #[test]
    fn empirical_mean_is_none_before_any_pull() {
        let arm = Arm::new("a".into(), Posterior::uninformative());
        assert_eq!(arm.empirical_mean(), None);
    }

    #[test]
    fn empirical_mean_tracks_raw_rewards_not_the_posterior() {
        let mut rng = SmallRng::seed_from_u64(5);
        let mut arm = Arm::new("a".into(), Posterior::uninformative());
        let rule = UpdateRule::Binarize { threshold: 0.5 };

        for _ in 0..10 {
            arm.posterior.observe(&mut rng, 0.9, rule).unwrap();
            arm.cumulative_reward += 0.9;
        }

        assert!((arm.empirical_mean().unwrap() - 0.9).abs() < 1e-9);
        // Binarising 0.9 into a success loses the magnitude: the posterior
        // climbs toward 1.0 while the true reward rate is 0.9.
        assert!(arm.posterior.mean() > 0.9);
    }
}
