// Package harness is a first-class simulation harness for thompson policies.
//
// It mirrors crates/thompson-sim's env/experiment/treatments as a Go library
// so custom policies, samplers, and scenarios can be evaluated without forking
// the harness. See thompson-sim/src/lib.rs for the Rust counterpart.
package harness

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Schedule describes how an arm's true success probability evolves.
type Schedule struct {
	Kind   string
	P      float64
	Before float64
	After  float64
	At     int
}

func Constant(p float64) Schedule { return Schedule{Kind: "constant", P: p} }
func Switch(before, after float64, at int) Schedule {
	return Schedule{Kind: "switch", Before: before, After: after, At: at}
}

func (s Schedule) AtRound(t int) float64 {
	switch s.Kind {
	case "switch":
		if t < s.At {
			return s.Before
		}
		return s.After
	default:
		return s.P
	}
}

// ArmSpec is one synthetic arm.
type ArmSpec struct {
	ID            string
	AvailableFrom int
	Schedule      Schedule
}

func Fixed(id string, p float64) ArmSpec {
	return ArmSpec{ID: id, Schedule: Constant(p)}
}
func Arriving(id string, p float64, at int) ArmSpec {
	return ArmSpec{ID: id, AvailableFrom: at, Schedule: Constant(p)}
}
func Switching(id string, before, after float64, at int) ArmSpec {
	return ArmSpec{ID: id, Schedule: Switch(before, after, at)}
}

// RewardKind controls synthetic reward shape.
type RewardKind int

const (
	Bernoulli RewardKind = iota
	Graded
)

// Scenario is a named bandit environment with known ground truth.
type Scenario struct {
	Name        string
	Description string
	Arms        []ArmSpec
	Horizon     int
	RewardKind  RewardKind
	Spread      float64
}

func (s *Scenario) Available(t int) []ArmSpec {
	var out []ArmSpec
	for _, a := range s.Arms {
		if a.AvailableFrom <= t {
			out = append(out, a)
		}
	}
	return out
}

func (s *Scenario) ArrivalsAt(t int) []ArmSpec {
	var out []ArmSpec
	for _, a := range s.Arms {
		if a.AvailableFrom == t {
			out = append(out, a)
		}
	}
	return out
}

func (s *Scenario) Mean(id string, t int) float64 {
	for _, a := range s.Arms {
		if a.ID == id {
			return a.Schedule.AtRound(t)
		}
	}
	return 0
}

func (s *Scenario) BestMean(t int) float64 {
	best := -1.0
	for _, a := range s.Available(t) {
		if m := a.Schedule.AtRound(t); m > best {
			best = m
		}
	}
	return best
}

func (s *Scenario) IsOptimal(id string, t int) bool {
	diff := s.Mean(id, t) - s.BestMean(t)
	return diff > -1e-12 && diff < 1e-12
}

func (s *Scenario) Draw(rng *rand.Rand, id string, t int) float64 {
	mean := s.Mean(id, t)
	switch s.RewardKind {
	case Graded:
		noise := (rng.Float64() - 0.5) * 2 * s.Spread
		v := mean + noise
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		return v
	default:
		if rng.Float64() < mean {
			return 1
		}
		return 0
	}
}

// Scenarios returns the built-in scenario set.
func Scenarios() []Scenario {
	return []Scenario{
		{
			Name: "easy", Description: "Three well-separated arms. Any working bandit should solve this.",
			Arms:    []ArmSpec{Fixed("openai/gpt-4", 0.90), Fixed("anthropic/claude-3-opus", 0.55), Fixed("meta/llama-3", 0.20)},
			Horizon: 5000, RewardKind: Bernoulli,
		},
		{
			Name: "hard", Description: "Five near-identical arms. Separating them needs real exploration.",
			Arms:    []ArmSpec{Fixed("openai/gpt-4", 0.50), Fixed("openai/gpt-4-turbo", 0.48), Fixed("anthropic/claude-3-opus", 0.47), Fixed("anthropic/claude-3-haiku", 0.46), Fixed("meta/llama-3", 0.45)},
			Horizon: 20000, RewardKind: Bernoulli,
		},
		{
			Name: "drift", Description: "Best and worst arms swap at the midpoint. Stale evidence is a trap.",
			Arms:    []ArmSpec{Switching("openai/gpt-4", 0.80, 0.30, 7500), Fixed("anthropic/claude-3-opus", 0.50), Switching("meta/llama-3", 0.30, 0.80, 7500)},
			Horizon: 15000, RewardKind: Bernoulli,
		},
		{
			Name: "churn", Description: "A better model ships mid-run. Cold-start cost is the whole story.",
			Arms:    []ArmSpec{Fixed("openai/gpt-4", 0.60), Fixed("anthropic/claude-3-opus", 0.55), Fixed("meta/llama-3", 0.30), Arriving("openai/gpt-4.5-turbo", 0.85, 3000)},
			Horizon: 10000, RewardKind: Bernoulli,
		},
		{
			Name: "graded", Description: "Continuous rewards that a success threshold flattens into a tie.",
			Arms:    []ArmSpec{Fixed("openai/gpt-4", 0.95), Fixed("anthropic/claude-3-opus", 0.70), Fixed("meta/llama-3", 0.35)},
			Horizon: 10000, RewardKind: Graded, Spread: 0.05,
		},
	}
}

// Treatment is a named policy configuration under test.
type Treatment struct {
	Label string
	Group string
	Build func() *thompson.Policy
}

// RunResult is the outcome of one run.
type RunResult struct {
	Regret           float64
	OptimalShare     float64
	NanosPerDecision float64
}

// Run executes one policy against one scenario with a given seed.
func Run(scenario *Scenario, treatment *Treatment, seed uint64) RunResult {
	policy := treatment.Build()
	rng := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))

	var regret float64
	optimalRounds := 0
	var selectTime time.Duration

	for t := 0; t < scenario.Horizon; t++ {
		for _, spec := range scenario.ArrivalsAt(t) {
			policy.AddArm(spec.ID)
		}
		started := time.Now()
		chosen, err := policy.Select(rng)
		if err != nil {
			panic(err)
		}
		selectTime += time.Since(started)

		reward := scenario.Draw(rng, chosen, t)
		if err := policy.Record(rng, chosen, reward); err != nil {
			panic(err)
		}
		regret += scenario.BestMean(t) - scenario.Mean(chosen, t)
		if scenario.IsOptimal(chosen, t) {
			optimalRounds++
		}
	}

	return RunResult{
		Regret:           regret,
		OptimalShare:     float64(optimalRounds) / float64(scenario.Horizon),
		NanosPerDecision: float64(selectTime.Nanoseconds()) / float64(scenario.Horizon),
	}
}

// Summary aggregates several seeds.
type Summary struct {
	Scenario         string
	Treatment        string
	Group            string
	Seeds            int
	MeanRegret       float64
	StderrRegret     float64
	MeanOptimalShare float64
	NanosPerDecision float64
}

func (s Summary) RegretCI95() float64 { return 1.96 * s.StderrRegret }

// Evaluate runs Seeds independent runs and summarises.
func Evaluate(scenario *Scenario, treatment *Treatment, seeds int) Summary {
	results := make([]RunResult, seeds)
	for i := 0; i < seeds; i++ {
		results[i] = Run(scenario, treatment, 0x5EED0000+uint64(i))
	}
	n := float64(seeds)
	var meanRegret float64
	for _, r := range results {
		meanRegret += r.Regret
	}
	meanRegret /= n

	var stderr float64
	if seeds > 1 {
		var variance float64
		for _, r := range results {
			variance += (r.Regret - meanRegret) * (r.Regret - meanRegret)
		}
		variance /= n - 1
		stderr = variance / n
		// Standard error of the mean.
		if stderr > 0 {
			stderr = math.Sqrt(stderr)
		}
	} else {
		stderr = nan()
	}

	var meanOptimal, meanNanos float64
	for _, r := range results {
		meanOptimal += r.OptimalShare
		meanNanos += r.NanosPerDecision
	}
	meanOptimal /= n
	meanNanos /= n

	return Summary{
		Scenario:         scenario.Name,
		Treatment:        treatment.Label,
		Group:            treatment.Group,
		Seeds:            seeds,
		MeanRegret:       meanRegret,
		StderrRegret:     stderr,
		MeanOptimalShare: meanOptimal,
		NanosPerDecision: meanNanos,
	}
}

func nan() float64 { return math.NaN() }
