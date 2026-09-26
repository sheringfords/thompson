package thompson

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
)

// ErrNoArms is returned by Select when no arms are registered.
var ErrNoArms = errors.New("thompson: no arms registered")

// ErrNilRNG is returned when a caller passes a nil random number generator.
// Sampling and Bernoulli updates dereference the RNG; failing explicitly
// beats a nil-pointer panic deep inside a selection or update.
var ErrNilRNG = errors.New("thompson: nil random number generator")

// SelectionKind selects how an arm is chosen from the current posteriors.
type SelectionKind int

const (
	// ThompsonSelection draws once per arm and takes the argmax.
	ThompsonSelection SelectionKind = iota

	// UCBRegularized adds a UCB-style bonus to under-explored arms.
	//
	// Redundant under an exact sampler, which already explores optimally in the
	// Bayesian sense. It earns its keep only when paired with an approximate
	// sampler that under-explores, which is the usual reason it appears.
	UCBRegularized

	// PhasedSelection round-robins every arm to a fixed pull count, then
	// samples as normal. Cold-start protection for settings where an unmeasured
	// arm is genuinely dangerous rather than merely unknown.
	PhasedSelection
)

// Selection configures the selection strategy.
type Selection struct {
	Kind SelectionKind
	// C is the UCB bonus coefficient.
	C float64
	// UntilPulls is the pull count at which the UCB bonus stops applying.
	UntilPulls uint64
	// Bootstrap and MinPullsForExploit gate PhasedSelection. The effective
	// quota is the larger of the two.
	Bootstrap          uint64
	MinPullsForExploit uint64
}

// Config configures a Policy.
type Config struct {
	UpdateRule UpdateRule
	Reward     RewardPolicy
	WarmStart  WarmStart
	Selection  Selection
	// Discount, when non-zero, is applied to every posterior after each
	// observation. Set it when arm quality drifts.
	Discount float64
}

// DefaultConfig returns a stationary, exact, family-warm-started configuration.
func DefaultConfig() Config {
	return Config{
		UpdateRule: DefaultUpdateRule(),
		Reward:     DefaultRewardPolicy(),
		WarmStart:  DefaultWarmStart(),
		Selection:  Selection{Kind: ThompsonSelection},
	}
}

// Arm is one selectable option and everything learned about it.
type Arm struct {
	ID               string    `json:"id"`
	Posterior        Posterior `json:"posterior"`
	CumulativeReward float64   `json:"cumulative_reward"`
	WarmStarted      bool      `json:"warm_started"`
}

// EmpiricalMean returns the mean of raw rewards observed, and whether the arm
// has been pulled. It differs from the posterior mean: it is unaffected by the
// prior and by the update rule's discretisation.
func (a *Arm) EmpiricalMean() (float64, bool) {
	if a.Posterior.Pulls == 0 {
		return 0, false
	}
	return a.CumulativeReward / float64(a.Posterior.Pulls), true
}

// ArmStats is a read-only summary of an arm.
type ArmStats struct {
	ID             string  `json:"id"`
	Alpha          float64 `json:"alpha"`
	Beta           float64 `json:"beta"`
	Pulls          uint64  `json:"pulls"`
	PosteriorMean  float64 `json:"posterior_mean"`
	EmpiricalMean  float64 `json:"empirical_mean"`
	HasObservation bool    `json:"has_observation"`
	CredibleWidth  float64 `json:"credible_width"`
	WarmStarted    bool    `json:"warm_started"`
}

// Policy is a Thompson Sampling policy over a mutable set of arms. It is safe
// for concurrent use.
//
// A single mutex guards everything. The obvious alternative — a policy lock
// plus a lock per arm — invites a lock-order inversion the moment one method
// takes them policy-first and another takes them arm-first, and that deadlock
// only shows up under production concurrency. Arms are never handed out by
// pointer to callers, so one lock is sufficient and cheap.
type Policy struct {
	mu         sync.Mutex
	arms       map[string]*Arm
	order      []string // sorted arm IDs, so iteration is deterministic
	config     Config
	sampler    Sampler
	totalPulls uint64
	observer   Observer
}

// New creates an empty policy.
func New(config Config, sampler Sampler) *Policy {
	if sampler == nil {
		sampler = ExactSampler{}
	}
	return &Policy{
		arms:    make(map[string]*Arm),
		config:  config,
		sampler: sampler,
	}
}

// NewDefault creates a policy with default configuration, the exact sampler,
// and the given arms.
func NewDefault(armIDs ...string) *Policy {
	p := New(DefaultConfig(), ExactSampler{})
	for _, id := range armIDs {
		p.AddArm(id)
	}
	return p
}

// AddArm registers an arm using the configured warm-start strategy, returning
// the prior applied and whether the arm was new.
func (p *Policy) AddArm(id string) (InformedPrior, bool) {
	p.mu.Lock()

	if _, exists := p.arms[id]; exists {
		p.mu.Unlock()
		return InformedPrior{}, false
	}
	prior := priorFor(p.config.WarmStart, id, p.order, p.arms)
	p.insertLocked(id, prior)
	warmStarted := p.arms[id].WarmStarted
	obs := p.observer
	p.mu.Unlock()
	// Outside the lock: observers may call back into the policy.
	if obs != nil {
		obs.OnArmAdded(id, warmStarted)
	}
	return prior, true
}

// AddArmWithPrior registers an arm with an explicit prior, overriding the
// warm-start strategy. It reports whether the arm was new.
func (p *Policy) AddArmWithPrior(id string, prior InformedPrior) bool {
	p.mu.Lock()

	if _, exists := p.arms[id]; exists {
		p.mu.Unlock()
		return false
	}
	p.insertLocked(id, prior)
	warmStarted := p.arms[id].WarmStarted
	obs := p.observer
	p.mu.Unlock()
	if obs != nil {
		obs.OnArmAdded(id, warmStarted)
	}
	return true
}

func (p *Policy) insertLocked(id string, prior InformedPrior) {
	warmStarted := prior != NewInformedPrior(1, 1)
	p.arms[id] = &Arm{
		ID:          id,
		Posterior:   prior.Posterior(),
		WarmStarted: warmStarted,
	}
	p.order = append(p.order, id)
	sort.Strings(p.order)
	// No notification here: callers (AddArm, AddArmWithPrior) notify after
	// unlocking, exactly once. Notifying under the caller's lock would
	// deadlock reentrant observers.
}

// SetObserver attaches an observer for metrics/logging. Nil clears it.
func (p *Policy) SetObserver(obs Observer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observer = obs
}

// DiscountPolicy returns the current discount as a first-class policy.
func (p *Policy) DiscountPolicy() FixedDiscount {
	return NewFixedDiscount(p.config.Discount)
}

// EffectiveMemory returns 1/(1-factor) or Inf when stationary.
func (p *Policy) EffectiveMemory() float64 {
	return p.DiscountPolicy().EffectiveMemory()
}

// SaveToStore persists via a SnapshotStore.
func (p *Policy) SaveToStore(store SnapshotStore) error {
	return store.Save(p.Snapshot())
}

// RemoveArm removes an arm, reporting whether it was present.
func (p *Policy) RemoveArm(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.arms[id]; !exists {
		return false
	}
	delete(p.arms, id)
	for i, existing := range p.order {
		if existing == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	return true
}

// HasArm reports whether an arm is registered.
func (p *Policy) HasArm(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, exists := p.arms[id]
	return exists
}

// Len returns the number of registered arms.
func (p *Policy) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.arms)
}

// TotalPulls returns the total observations recorded across all arms.
func (p *Policy) TotalPulls() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.totalPulls
}

// LoggingPolicyID derives the behavior-policy identifier from the actual
// active configuration: selection kind plus sampler name. It is the single
// source for DecisionStarted.LoggingPolicyID and the shadow events'
// PrimaryLoggingPolicyID, so live and shadow paths can never disagree (audit
// A3). Only "exact-thompson-v1" describes the exact-Thompson argmax that the
// offline propensity estimators model; every other configuration yields an ID
// the OPE reliability gate refuses as a denominator. Historical records keep
// whatever string they were written with.
func (p *Policy) LoggingPolicyID() string {
	p.mu.Lock()
	kind := p.config.Selection.Kind
	p.mu.Unlock()
	switch kind {
	case UCBRegularized:
		return "ucb-regularized-v1"
	case PhasedSelection:
		return "phased-v1"
	default:
		if p.sampler.Name() == "exact" {
			return "exact-thompson-v1"
		}
		return "thompson-" + p.sampler.Name() + "-v1"
	}
}

// SamplerName returns the active sampler's name.
func (p *Policy) SamplerName() string { return p.sampler.Name() }

// EligibleArmIDs returns a copy of the current sorted eligible arm IDs.
// Deterministic iteration order; safe to call concurrently.
func (p *Policy) EligibleArmIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.order))
	copy(out, p.order)
	return out
}

// PosteriorFor returns a copy of the posterior for the given arm, if present.
// Used to capture immutable before/after snapshots for evidence without
// exposing internal pointers. Caller must not mutate returned value.
func (p *Policy) PosteriorFor(id string) (Posterior, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.arms[id]
	if !ok {
		return Posterior{}, false
	}
	return a.Posterior, true
}

// ConfigSnapshot returns a copy of current Config for hashing/evidence.
func (p *Policy) ConfigSnapshot() Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.config
}

// hashConfig returns a stable hex hash of a policy config (JSON canonical).
// Errors are impossible with known structs; on marshal error returns empty.
func hashConfig(cfg Config) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8]) // 16 hex chars sufficient for V0
}

// ConfigHash returns a stable hex hash of the policy config (JSON canonical).
// Used for DecisionStarted.policy_config_hash. Errors are impossible with
// known structs; on marshal error returns empty.
func (p *Policy) ConfigHash() string {
	p.mu.Lock()
	cfg := p.config
	p.mu.Unlock()
	return hashConfig(cfg)
}

// SelectWithScores atomically selects an arm and returns the actual sampled
// scores map used for the decision. The map is newly allocated per call.
// Scores are true Beta samples (or, when Phased forced, posterior means) —
// never stale means. This is the evidence path for DecisionStarted.sampled_scores.
func (p *Policy) SelectWithScores(rng *rand.Rand) (string, map[string]float64, error) {
	if rng == nil {
		return "", nil, ErrNilRNG
	}
	p.mu.Lock()
	if len(p.arms) == 0 {
		p.mu.Unlock()
		return "", nil, ErrNoArms
	}
	scores := make(map[string]float64, len(p.order))
	var chosen string
	switch p.config.Selection.Kind {
	case UCBRegularized:
		if err := p.checkUCBLocked(); err != nil {
			p.mu.Unlock()
			return "", nil, err
		}
		chosen, scores = p.argmaxUCBWithScoresLocked(rng, scores)
	case PhasedSelection:
		quota := p.config.Selection.Bootstrap
		if p.config.Selection.MinPullsForExploit > quota {
			quota = p.config.Selection.MinPullsForExploit
		}
		if id, ok := p.leastPulledBelowLocked(quota); ok {
			chosen = id
			for _, pid := range p.order {
				scores[pid] = p.arms[pid].Posterior.Mean()
			}
		} else {
			chosen, scores = p.argmaxSampledWithScoresLocked(rng, scores)
		}
	default:
		chosen, scores = p.argmaxSampledWithScoresLocked(rng, scores)
	}
	obs := p.observer
	p.mu.Unlock()
	if obs != nil {
		// Notify outside the policy lock: observers that inspect the policy
		// (Stats, Select) or trigger downstream learning would otherwise
		// self-deadlock on the non-reentrant mutex. The observer gets its
		// own copy; the caller keeps the original.
		obsScores := make(map[string]float64, len(scores))
		for id, s := range scores {
			obsScores[id] = s
		}
		obs.OnSelect(chosen, obsScores)
	}
	return chosen, scores, nil
}

// Select chooses an arm.
//
// It does not mutate learned state: nothing is learned until the outcome comes
// back through Record or RecordOutcome. Selecting without recording is
// legitimate — a request may be cancelled — and simply teaches nothing.
func (p *Policy) Select(rng *rand.Rand) (string, error) {
	if rng == nil {
		return "", ErrNilRNG
	}
	p.mu.Lock()

	if len(p.arms) == 0 {
		p.mu.Unlock()
		return "", ErrNoArms
	}

	var chosen string
	var scores map[string]float64
	if p.observer != nil {
		scores = make(map[string]float64, len(p.order))
	}
	switch p.config.Selection.Kind {
	case UCBRegularized:
		if err := p.checkUCBLocked(); err != nil {
			p.mu.Unlock()
			return "", err
		}
		if p.observer != nil {
			chosen, scores = p.argmaxUCBWithScoresLocked(rng, scores)
		} else {
			chosen = p.argmaxUCBLocked(rng)
		}
	case PhasedSelection:
		quota := p.config.Selection.Bootstrap
		if p.config.Selection.MinPullsForExploit > quota {
			quota = p.config.Selection.MinPullsForExploit
		}
		if id, ok := p.leastPulledBelowLocked(quota); ok {
			chosen = id
			if p.observer != nil {
				for _, pid := range p.order {
					scores[pid] = p.arms[pid].Posterior.Mean()
				}
			}
		} else {
			if p.observer != nil {
				chosen, scores = p.argmaxSampledWithScoresLocked(rng, scores)
			} else {
				chosen = p.argmaxSampledLocked(rng)
			}
		}
	default:
		if p.observer != nil {
			chosen, scores = p.argmaxSampledWithScoresLocked(rng, scores)
		} else {
			chosen = p.argmaxSampledLocked(rng)
		}
	}

	obs := p.observer
	p.mu.Unlock()
	if obs != nil {
		// Outside the lock (see SelectWithScores): observers may re-enter.
		obsScores := make(map[string]float64, len(scores))
		for id, s := range scores {
			obsScores[id] = s
		}
		obs.OnSelect(chosen, obsScores)
	}

	return chosen, nil
}

// SelectWith chooses an arm using a custom SelectionStrategy, bypassing
// config.Selection. This is the first-class extension point for selection.
//
// Isolation: the strategy receives deep copies (fresh Arm values and a fresh
// order slice) snapshotted under the policy lock, and runs after the lock is
// released. A malicious or buggy strategy can neither mutate live posteriors
// nor retain usable references to internal state, and it cannot deadlock the
// policy by calling back into it. The public signature is unchanged.
func (p *Policy) SelectWith(rng *rand.Rand, strategy SelectionStrategy) (string, error) {
	if rng == nil {
		return "", ErrNilRNG
	}
	if strategy == nil {
		return "", fmt.Errorf("thompson: nil selection strategy")
	}
	p.mu.Lock()
	if len(p.arms) == 0 {
		p.mu.Unlock()
		return "", ErrNoArms
	}
	armsCopy := make(map[string]*Arm, len(p.arms))
	for id, a := range p.arms {
		clone := *a
		armsCopy[id] = &clone
	}
	orderCopy := append([]string(nil), p.order...)
	sampler := p.sampler
	total := p.totalPulls
	obs := p.observer
	p.mu.Unlock()

	chosen := strategy.Select(rng, armsCopy, orderCopy, sampler, total)
	if _, ok := armsCopy[chosen]; !ok {
		return "", fmt.Errorf("thompson: strategy selected unknown arm %q", chosen)
	}
	if obs != nil {
		scores := make(map[string]float64, len(armsCopy))
		for id, a := range armsCopy {
			scores[id] = a.Posterior.Mean()
		}
		obs.OnSelect(chosen, scores)
	}
	return chosen, nil
}

// SelectionStrategyFromConfig returns a strategy matching config.Selection.
func (p *Policy) SelectionStrategyFromConfig() SelectionStrategy {
	switch p.config.Selection.Kind {
	case UCBRegularized:
		return UCBRegularizedStrategy{C: p.config.Selection.C, UntilPulls: p.config.Selection.UntilPulls}
	case PhasedSelection:
		return PhasedStrategy{Bootstrap: p.config.Selection.Bootstrap, MinPullsForExploit: p.config.Selection.MinPullsForExploit}
	default:
		return ThompsonStrategy{}
	}
}

func (p *Policy) argmaxSampledLocked(rng *rand.Rand) string {
	best, bestScore := "", math.Inf(-1)
	for _, id := range p.order {
		score := p.sampler.Sample(rng, p.arms[id].Posterior)
		if best == "" || score > bestScore {
			best, bestScore = id, score
		}
	}
	return best
}

func (p *Policy) argmaxSampledWithScoresLocked(rng *rand.Rand, scores map[string]float64) (string, map[string]float64) {
	best, bestScore := "", math.Inf(-1)
	for _, id := range p.order {
		score := p.sampler.Sample(rng, p.arms[id].Posterior)
		scores[id] = score
		if best == "" || score > bestScore {
			best, bestScore = id, score
		}
	}
	return best, scores
}

// checkUCBLocked rejects a non-finite UCB bonus coefficient. NaN poisons
// every score comparison (all false), silently degenerating selection to
// first-arm-wins; +Inf pins every under-explored arm at infinity.
func (p *Policy) checkUCBLocked() error {
	c := p.config.Selection.C
	if math.IsNaN(c) || math.IsInf(c, 0) {
		return fmt.Errorf("thompson: invalid UCB coefficient %v", c)
	}
	return nil
}

func (p *Policy) argmaxUCBWithScoresLocked(rng *rand.Rand, scores map[string]float64) (string, map[string]float64) {
	logTotal := math.Log(float64(p.totalPulls + 1))
	sel := p.config.Selection
	best, bestScore := "", math.Inf(-1)
	for _, id := range p.order {
		arm := p.arms[id]
		samp := p.sampler.Sample(rng, arm.Posterior)
		var score float64
		switch {
		case arm.Posterior.Pulls >= sel.UntilPulls:
			score = samp
		case arm.Posterior.Pulls == 0:
			score = math.Inf(1)
		default:
			score = samp + sel.C*math.Sqrt(logTotal/float64(arm.Posterior.Pulls))
		}
		scores[id] = score
		if best == "" || score > bestScore {
			best, bestScore = id, score
		}
	}
	return best, scores
}

func (p *Policy) argmaxUCBLocked(rng *rand.Rand) string {
	// Log(totalPulls) is undefined at zero and negative at one, either of which
	// poisons every comparison with NaN. Shifting by one keeps the bonus finite
	// and non-negative from the very first round.
	logTotal := math.Log(float64(p.totalPulls + 1))
	sel := p.config.Selection

	best, bestScore := "", math.Inf(-1)
	for _, id := range p.order {
		arm := p.arms[id]
		score := p.sampler.Sample(rng, arm.Posterior)
		switch {
		case arm.Posterior.Pulls >= sel.UntilPulls:
			// no bonus
		case arm.Posterior.Pulls == 0:
			score = math.Inf(1)
		default:
			score += sel.C * math.Sqrt(logTotal/float64(arm.Posterior.Pulls))
		}
		if best == "" || score > bestScore {
			best, bestScore = id, score
		}
	}
	return best
}

func (p *Policy) leastPulledBelowLocked(threshold uint64) (string, bool) {
	best, bestPulls := "", uint64(math.MaxUint64)
	for _, id := range p.order {
		pulls := p.arms[id].Posterior.Pulls
		if pulls < threshold && pulls < bestPulls {
			best, bestPulls = id, pulls
		}
	}
	return best, best != ""
}

// Record folds a raw reward in [0, 1] into an arm's posterior.
func (p *Policy) Record(rng *rand.Rand, id string, reward float64) error {
	if rng == nil {
		return ErrNilRNG
	}
	p.mu.Lock()

	arm, exists := p.arms[id]
	if !exists {
		p.mu.Unlock()
		return fmt.Errorf("thompson: unknown arm %q", id)
	}
	if err := arm.Posterior.Observe(rng, reward, p.config.UpdateRule); err != nil {
		p.mu.Unlock()
		return err
	}
	arm.CumulativeReward += reward
	posteriorSnap := arm.Posterior
	p.totalPulls++

	discount := NewFixedDiscount(p.config.Discount)
	discounted := false
	if factor := discount.Factor(); factor > 0 {
		for _, other := range p.arms {
			other.Posterior.Discount(factor)
		}
		discounted = true
	}
	obs := p.observer
	discountFactor := discount.Factor()
	p.mu.Unlock()

	// Notifications outside the lock (see SelectWithScores).
	if discounted && obs != nil {
		obs.OnDiscount(discountFactor)
	}
	if obs != nil {
		obs.OnRecord(id, reward, posteriorSnap)
	}
	return nil
}

// RestoreFromStore rebuilds a policy from a SnapshotStore.
func RestoreFromStore(store SnapshotStore, config Config, sampler Sampler) (*Policy, error) {
	snap, err := store.Load()
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, nil
	}
	return Restore(*snap, config, sampler)
}

// RecordOutcome scores an outcome through the reward policy and records it.
func (p *Policy) RecordOutcome(rng *rand.Rand, id string, outcome Outcome) error {
	if err := p.config.Reward.Weights.Validate(); err != nil {
		return err
	}
	return p.Record(rng, id, p.config.Reward.Reward(outcome))
}

// Stats returns per-arm summaries, best posterior mean first.
func (p *Policy) Stats() []ArmStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	stats := make([]ArmStats, 0, len(p.order))
	for _, id := range p.order {
		arm := p.arms[id]
		mean, ok := arm.EmpiricalMean()
		stats = append(stats, ArmStats{
			ID:             arm.ID,
			Alpha:          arm.Posterior.Alpha,
			Beta:           arm.Posterior.Beta,
			Pulls:          arm.Posterior.Pulls,
			PosteriorMean:  arm.Posterior.Mean(),
			EmpiricalMean:  mean,
			HasObservation: ok,
			CredibleWidth:  arm.Posterior.CredibleWidth(),
			WarmStarted:    arm.WarmStarted,
		})
	}
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].PosteriorMean != stats[j].PosteriorMean {
			return stats[i].PosteriorMean > stats[j].PosteriorMean
		}
		return stats[i].ID < stats[j].ID
	})
	return stats
}

// BestArm returns the arm with the highest posterior mean among those with at
// least minPulls observations.
func (p *Policy) BestArm(minPulls uint64) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	best, bestMean := "", math.Inf(-1)
	for _, id := range p.order {
		arm := p.arms[id]
		if arm.Posterior.Pulls < minPulls {
			continue
		}
		if mean := arm.Posterior.Mean(); best == "" || mean > bestMean {
			best, bestMean = id, mean
		}
	}
	return best, best != ""
}

// RestoreSnapshot replaces the policy's learned state in place from a
// snapshot, keeping the active sampler and observer. It validates like
// Restore and swaps arms, config and totals atomically under the policy
// lock, so learners can re-fold history (corrections, crash recovery)
// without handing out a new Policy or copying a mutex.
func (p *Policy) RestoreSnapshot(snapshot Snapshot) error {
	if snapshot.Version != SnapshotVersion {
		return fmt.Errorf("thompson: unsupported snapshot version %d (expected %d)",
			snapshot.Version, SnapshotVersion)
	}
	arms := make(map[string]*Arm, len(snapshot.Arms))
	var order []string
	for _, arm := range snapshot.Arms {
		if _, err := NewPosterior(arm.Posterior.Alpha, arm.Posterior.Beta); err != nil {
			return fmt.Errorf("thompson: arm %q: %w", arm.ID, err)
		}
		if _, dup := arms[arm.ID]; dup {
			return fmt.Errorf("thompson: snapshot names arm %q twice", arm.ID)
		}
		clone := arm
		arms[arm.ID] = &clone
		order = append(order, arm.ID)
	}
	sort.Strings(order)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.arms = arms
	p.order = order
	if snapshot.Config != nil {
		p.config = *snapshot.Config
	}
	p.totalPulls = snapshot.TotalPulls
	return nil
}

// SnapshotVersion is the current snapshot format version.
const SnapshotVersion = 1

// Snapshot is a serialisable capture of a policy's learned state.
// Config is optional for wire compatibility with Rust `policy.rs:540` which
// embeds Config; when present, `Restore` prefers it over the passed `config`
// param. Existing Go snapshots without `config` remain valid.
type Snapshot struct {
	Version    uint32  `json:"version"`
	Config     *Config `json:"config"`
	Arms       []Arm   `json:"arms"`
	TotalPulls uint64  `json:"total_pulls"`
}

// Snapshot captures the policy's learned state, including Config for
// cross-language wire compatibility (Rust includes Config).
func (p *Policy) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()

	arms := make([]Arm, 0, len(p.order))
	for _, id := range p.order {
		arms = append(arms, *p.arms[id])
	}
	cfg := p.config
	return Snapshot{Version: SnapshotVersion, Config: &cfg, Arms: arms, TotalPulls: p.totalPulls}
}

// Restore rebuilds a policy from a snapshot. If snapshot.Config is non-nil it
// takes precedence, otherwise the passed `config` is used (backwards compat).
func Restore(snapshot Snapshot, config Config, sampler Sampler) (*Policy, error) {
	if snapshot.Version != SnapshotVersion {
		return nil, fmt.Errorf("thompson: unsupported snapshot version %d (expected %d)",
			snapshot.Version, SnapshotVersion)
	}
	if snapshot.Config != nil {
		config = *snapshot.Config
	}

	p := New(config, sampler)
	for _, arm := range snapshot.Arms {
		if _, err := NewPosterior(arm.Posterior.Alpha, arm.Posterior.Beta); err != nil {
			return nil, fmt.Errorf("thompson: arm %q: %w", arm.ID, err)
		}
		// A snapshot is deserialised input, so it can name the same arm twice.
		// Accepting that would put the ID in order twice against one map entry,
		// and the arm would then be sampled twice in every selection.
		if _, dup := p.arms[arm.ID]; dup {
			return nil, fmt.Errorf("thompson: snapshot names arm %q twice", arm.ID)
		}
		clone := arm
		p.arms[arm.ID] = &clone
		p.order = append(p.order, arm.ID)
	}
	sort.Strings(p.order)
	p.totalPulls = snapshot.TotalPulls
	return p, nil
}
