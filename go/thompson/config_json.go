package thompson

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Canonical protocol-v1 config JSON.
//
// The canonical shape is byte-compatible with the Rust serde output, so a
// snapshot written by either language restores in the other:
//
//	update_rule: {"rule":"bernoulli"} | {"rule":"binarize","threshold":T}
//	            | {"rule":"fractional"}
//	reward_policy: {"weights":{"latency","success","cache","cost","quality"},
//	            "target_latency_ms","max_latency_ms","target_cost_usd",
//	            "max_cost_usd","failure_is_zero"}
//	warm_start: {"strategy":"cold"} | {"strategy":"fixed","alpha","beta"}
//	            | {"strategy":"family_similarity","discount",
//	               "fallback":{"alpha","beta"}}
//	selection: {"selection":"thompson"}
//	            | {"selection":"ucb_regularized","c","until_pulls"}
//	            | {"selection":"phased","bootstrap","min_pulls_for_exploit"}
//	discount: null (stationary) or a number in (0, 1).
//
// Mapping notes, all deliberate and tested by the protocol golden tests:
//   - Go Discount 0 means stationary and marshals as null; null unmarshals
//     to 0. Rust None behaves identically.
//   - Go WarmStart carries Fixed/Discount/Fallback fields for every kind;
//     only the fields belonging to the active kind are emitted.
//   - Unknown JSON fields are ignored on decode by both languages
//     (forward-compatible; never required).
//   - Floats round-trip with up-to-ULP error through JSON; snapshot
//     comparisons must use tolerance, never ==.
//
// Legacy readers: UnmarshalJSON also accepts the pre-canonical Go shape
// (PascalCase fields, integer Kind enums). Writers always emit canonical.
func (c Config) MarshalJSON() ([]byte, error) {
	type weightsJSON struct {
		Latency float64 `json:"latency"`
		Success float64 `json:"success"`
		Cache   float64 `json:"cache"`
		Cost    float64 `json:"cost"`
		Quality float64 `json:"quality"`
	}
	type rewardJSON struct {
		Weights         weightsJSON `json:"weights"`
		TargetLatencyMs float64     `json:"target_latency_ms"`
		MaxLatencyMs    float64     `json:"max_latency_ms"`
		TargetCostUSD   float64     `json:"target_cost_usd"`
		MaxCostUSD      float64     `json:"max_cost_usd"`
		FailureIsZero   bool        `json:"failure_is_zero"`
	}
	m := map[string]any{
		"update_rule": marshalUpdateRule(c.UpdateRule),
		"reward_policy": rewardJSON{
			Weights: weightsJSON{
				Latency: c.Reward.Weights.Latency, Success: c.Reward.Weights.Success,
				Cache: c.Reward.Weights.Cache, Cost: c.Reward.Weights.Cost,
				Quality: c.Reward.Weights.Quality,
			},
			TargetLatencyMs: c.Reward.TargetLatencyMs, MaxLatencyMs: c.Reward.MaxLatencyMs,
			TargetCostUSD: c.Reward.TargetCostUSD, MaxCostUSD: c.Reward.MaxCostUSD,
			FailureIsZero: c.Reward.FailureIsZero,
		},
		"warm_start": marshalWarmStart(c.WarmStart),
		"selection":  marshalSelection(c.Selection),
	}
	if c.Discount == 0 {
		m["discount"] = nil
	} else {
		m["discount"] = c.Discount
	}
	return json.Marshal(m)
}

func marshalUpdateRule(r UpdateRule) map[string]any {
	switch r.Kind {
	case Binarize:
		return map[string]any{"rule": "binarize", "threshold": r.Threshold}
	case Fractional:
		return map[string]any{"rule": "fractional"}
	default:
		return map[string]any{"rule": "bernoulli"}
	}
}

func marshalWarmStart(w WarmStart) map[string]any {
	switch w.Kind {
	case FixedPrior:
		return map[string]any{"strategy": "fixed", "alpha": w.Fixed.Alpha, "beta": w.Fixed.Beta}
	case FamilySimilarity:
		return map[string]any{"strategy": "family_similarity", "discount": w.Discount,
			"fallback": map[string]any{"alpha": w.Fallback.Alpha, "beta": w.Fallback.Beta}}
	default:
		return map[string]any{"strategy": "cold"}
	}
}

func marshalSelection(s Selection) map[string]any {
	switch s.Kind {
	case UCBRegularized:
		return map[string]any{"selection": "ucb_regularized", "c": s.C, "until_pulls": s.UntilPulls}
	case PhasedSelection:
		return map[string]any{"selection": "phased", "bootstrap": s.Bootstrap, "min_pulls_for_exploit": s.MinPullsForExploit}
	default:
		return map[string]any{"selection": "thompson"}
	}
}

// UnmarshalJSON accepts canonical protocol-v1 first, then the legacy Go
// shape (PascalCase keys, no config). Note the legacy shape is NOT
// decodable by struct tags alone: encoding/json matches keys
// case-insensitively but not underscore-insensitively, so
// "CumulativeReward"/"TotalPulls" need explicit legacy tags.
func (s *Snapshot) UnmarshalJSON(b []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if _, ok := probe["arms"]; !ok {
		return s.unmarshalLegacySnapshot(b)
	}
	type plain Snapshot
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = Snapshot(p)
	return nil
}

func (s *Snapshot) unmarshalLegacySnapshot(b []byte) error {
	type legacyArm struct {
		ID               string    `json:"ID"`
		Posterior        Posterior `json:"Posterior"`
		CumulativeReward float64   `json:"CumulativeReward"`
		WarmStarted      bool      `json:"WarmStarted"`
	}
	var wire struct {
		Version    uint32      `json:"Version"`
		Arms       []legacyArm `json:"Arms"`
		TotalPulls uint64      `json:"TotalPulls"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return fmt.Errorf("thompson: bad legacy snapshot: %w", err)
	}
	s.Version = wire.Version
	s.Config = nil
	s.TotalPulls = wire.TotalPulls
	s.Arms = make([]Arm, len(wire.Arms))
	for i, a := range wire.Arms {
		s.Arms[i] = Arm{ID: a.ID, Posterior: a.Posterior,
			CumulativeReward: a.CumulativeReward, WarmStarted: a.WarmStarted}
	}
	return nil
}
func (c *Config) UnmarshalJSON(b []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if _, ok := probe["update_rule"]; !ok {
		return c.unmarshalLegacy(b)
	}
	var wire struct {
		UpdateRule   json.RawMessage `json:"update_rule"`
		RewardPolicy json.RawMessage `json:"reward_policy"`
		WarmStart    json.RawMessage `json:"warm_start"`
		Selection    json.RawMessage `json:"selection"`
		Discount     *float64        `json:"discount"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	// Encoding/json ignores unknown fields: forward-compatible by default.
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("thompson: bad canonical config: %w", err)
	}
	ur, err := unmarshalUpdateRule(wire.UpdateRule)
	if err != nil {
		return err
	}
	rp, err := unmarshalRewardPolicy(wire.RewardPolicy)
	if err != nil {
		return err
	}
	ws, err := unmarshalWarmStart(wire.WarmStart)
	if err != nil {
		return err
	}
	sel, err := unmarshalSelection(wire.Selection)
	if err != nil {
		return err
	}
	c.UpdateRule, c.Reward, c.WarmStart, c.Selection = ur, rp, ws, sel
	c.Discount = 0
	if wire.Discount != nil {
		c.Discount = *wire.Discount
	}
	return nil
}

func strField(m map[string]json.RawMessage, key string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", fmt.Errorf("thompson: config missing %q", key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("thompson: config %q not a string", key)
	}
	return s, nil
}

func numField[V ~float64 | ~uint64](m map[string]json.RawMessage, key string, dst *V) error {
	raw, ok := m[key]
	if !ok {
		return fmt.Errorf("thompson: config missing %q", key)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("thompson: config %q not a number", key)
	}
	return nil
}

func unmarshalUpdateRule(b []byte) (UpdateRule, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return UpdateRule{}, fmt.Errorf("thompson: bad update_rule: %w", err)
	}
	rule, err := strField(m, "rule")
	if err != nil {
		return UpdateRule{}, err
	}
	switch rule {
	case "binarize":
		var r UpdateRule
		r.Kind = Binarize
		if err := numField(m, "threshold", &r.Threshold); err != nil {
			return UpdateRule{}, err
		}
		return r, nil
	case "fractional":
		return UpdateRule{Kind: Fractional}, nil
	case "bernoulli":
		return UpdateRule{Kind: Bernoulli}, nil
	default:
		return UpdateRule{}, fmt.Errorf("thompson: unknown update rule %q", rule)
	}
}

func unmarshalRewardPolicy(b []byte) (RewardPolicy, error) {
	var w struct {
		Weights struct {
			Latency float64 `json:"latency"`
			Success float64 `json:"success"`
			Cache   float64 `json:"cache"`
			Cost    float64 `json:"cost"`
			Quality float64 `json:"quality"`
		} `json:"weights"`
		TargetLatencyMs float64 `json:"target_latency_ms"`
		MaxLatencyMs    float64 `json:"max_latency_ms"`
		TargetCostUSD   float64 `json:"target_cost_usd"`
		MaxCostUSD      float64 `json:"max_cost_usd"`
		FailureIsZero   bool    `json:"failure_is_zero"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return RewardPolicy{}, fmt.Errorf("thompson: bad reward_policy: %w", err)
	}
	return RewardPolicy{
		Weights: Weights{
			Latency: w.Weights.Latency, Success: w.Weights.Success,
			Cache: w.Weights.Cache, Cost: w.Weights.Cost, Quality: w.Weights.Quality,
		},
		TargetLatencyMs: w.TargetLatencyMs, MaxLatencyMs: w.MaxLatencyMs,
		TargetCostUSD: w.TargetCostUSD, MaxCostUSD: w.MaxCostUSD,
		FailureIsZero: w.FailureIsZero,
	}, nil
}

func unmarshalWarmStart(b []byte) (WarmStart, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return WarmStart{}, fmt.Errorf("thompson: bad warm_start: %w", err)
	}
	strategy, err := strField(m, "strategy")
	if err != nil {
		return WarmStart{}, err
	}
	switch strategy {
	case "cold":
		return WarmStart{Kind: ColdStart}, nil
	case "fixed":
		var w WarmStart
		w.Kind = FixedPrior
		var prior struct {
			Alpha float64 `json:"alpha"`
			Beta  float64 `json:"beta"`
		}
		if err := json.Unmarshal(b, &prior); err != nil {
			return WarmStart{}, fmt.Errorf("thompson: bad fixed prior: %w", err)
		}
		w.Fixed = InformedPrior{Alpha: prior.Alpha, Beta: prior.Beta}
		return w, nil
	case "family_similarity":
		var w WarmStart
		w.Kind = FamilySimilarity
		var body struct {
			Discount float64 `json:"discount"`
			Fallback struct {
				Alpha float64 `json:"alpha"`
				Beta  float64 `json:"beta"`
			} `json:"fallback"`
		}
		if err := json.Unmarshal(b, &body); err != nil {
			return WarmStart{}, fmt.Errorf("thompson: bad family_similarity: %w", err)
		}
		w.Discount = body.Discount
		w.Fallback = InformedPrior{Alpha: body.Fallback.Alpha, Beta: body.Fallback.Beta}
		return w, nil
	default:
		return WarmStart{}, fmt.Errorf("thompson: unknown warm-start strategy %q", strategy)
	}
}

func unmarshalSelection(b []byte) (Selection, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return Selection{}, fmt.Errorf("thompson: bad selection: %w", err)
	}
	sel, err := strField(m, "selection")
	if err != nil {
		return Selection{}, err
	}
	switch sel {
	case "thompson":
		return Selection{Kind: ThompsonSelection}, nil
	case "ucb_regularized":
		var s Selection
		s.Kind = UCBRegularized
		if err := numField(m, "c", &s.C); err != nil {
			return Selection{}, err
		}
		if err := numField(m, "until_pulls", &s.UntilPulls); err != nil {
			return Selection{}, err
		}
		return s, nil
	case "phased":
		var s Selection
		s.Kind = PhasedSelection
		if err := numField(m, "bootstrap", &s.Bootstrap); err != nil {
			return Selection{}, err
		}
		if err := numField(m, "min_pulls_for_exploit", &s.MinPullsForExploit); err != nil {
			return Selection{}, err
		}
		return s, nil
	default:
		return Selection{}, fmt.Errorf("thompson: unknown selection %q", sel)
	}
}

// unmarshalLegacy decodes the pre-canonical Go shape: PascalCase fields
// with integer Kind enums and a numeric Discount (0 = stationary).
func (c *Config) unmarshalLegacy(b []byte) error {
	type legacyUpdateRule struct {
		Kind      UpdateKind `json:"Kind"`
		Threshold float64    `json:"Threshold"`
	}
	type legacyWarmStart struct {
		Kind     WarmStartKind `json:"Kind"`
		Fixed    InformedPrior `json:"Fixed"`
		Discount float64       `json:"Discount"`
		Fallback InformedPrior `json:"Fallback"`
	}
	type legacySelection struct {
		Kind               SelectionKind `json:"Kind"`
		C                  float64       `json:"C"`
		UntilPulls         uint64        `json:"UntilPulls"`
		Bootstrap          uint64        `json:"Bootstrap"`
		MinPullsForExploit uint64        `json:"MinPullsForExploit"`
	}
	var wire struct {
		UpdateRule legacyUpdateRule `json:"UpdateRule"`
		Reward     RewardPolicy     `json:"Reward"`
		WarmStart  legacyWarmStart  `json:"WarmStart"`
		Selection  legacySelection  `json:"Selection"`
		Discount   float64          `json:"Discount"`
	}
	// RewardPolicy/Weights have no JSON tags; encoding/json matches keys
	// case-insensitively, so the legacy PascalCase object decodes as-is.
	if err := json.Unmarshal(b, &wire); err != nil {
		return fmt.Errorf("thompson: bad legacy config: %w", err)
	}
	c.UpdateRule = UpdateRule{Kind: wire.UpdateRule.Kind, Threshold: wire.UpdateRule.Threshold}
	c.Reward = wire.Reward
	c.WarmStart = WarmStart{Kind: wire.WarmStart.Kind, Fixed: wire.WarmStart.Fixed,
		Discount: wire.WarmStart.Discount, Fallback: wire.WarmStart.Fallback}
	c.Selection = Selection{Kind: wire.Selection.Kind, C: wire.Selection.C,
		UntilPulls: wire.Selection.UntilPulls, Bootstrap: wire.Selection.Bootstrap,
		MinPullsForExploit: wire.Selection.MinPullsForExploit}
	c.Discount = wire.Discount
	return nil
}
