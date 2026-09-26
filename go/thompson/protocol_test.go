package thompson

// Protocol-v1 conformance: canonical JSON wire format shared with the Rust
// implementation. These TestProtocol* tests are the CI conformance gate
// (ci.yml runs `-run TestProtocol`); renaming them without updating CI
// silently disables the gate.

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testdataPath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	// file is go/thompson/protocol_test.go; fixtures live in protocol/testdata.
	return filepath.Join(filepath.Dir(file), "../../protocol/testdata", name)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(testdataPath(t, name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// TestProtocolCanonicalVariants covers every enum variant in both directions.
func TestProtocolCanonicalVariants(t *testing.T) {
	configs := []Config{
		DefaultConfig(),
		{
			UpdateRule: UpdateRule{Kind: Binarize, Threshold: 0.6},
			Reward:     DefaultRewardPolicy(),
			WarmStart:  WarmStart{Kind: FixedPrior, Fixed: NewInformedPrior(2, 3)},
			Selection:  Selection{Kind: UCBRegularized, C: 2.0, UntilPulls: 30},
			Discount:   0.99,
		},
		{
			UpdateRule: UpdateRule{Kind: Fractional},
			Reward:     DefaultRewardPolicy(),
			WarmStart: WarmStart{Kind: FamilySimilarity, Discount: 0.2,
				Fallback: NewInformedPrior(4, 1)},
			Selection: Selection{Kind: PhasedSelection, Bootstrap: 5, MinPullsForExploit: 10},
		},
		{
			UpdateRule: UpdateRule{Kind: Bernoulli},
			Reward:     DefaultRewardPolicy(),
			WarmStart:  WarmStart{Kind: ColdStart},
			Selection:  Selection{Kind: ThompsonSelection},
		},
	}
	for i, cfg := range configs {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("config %d marshal: %v", i, err)
		}
		var back Config
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("config %d canonical re-read: %v\n%s", i, err, b)
		}
		if back != cfg {
			t.Fatalf("config %d round trip diverged:\n%s\n%+v", i, b, back)
		}
	}
}

// TestProtocolRejectsUnknownEnums ensures forward-incompatible values fail
// loudly instead of decoding into a neighboring variant.
func TestProtocolRejectsUnknownEnums(t *testing.T) {
	bads := []string{
		`{"update_rule":{"rule":"nope"},"reward_policy":{"weights":{"latency":0,"success":1,"cache":0,"cost":0,"quality":0},"target_latency_ms":1,"max_latency_ms":2,"target_cost_usd":0,"max_cost_usd":1,"failure_is_zero":true},"warm_start":{"strategy":"cold"},"selection":{"selection":"thompson"},"discount":null}`,
		`{"update_rule":{"rule":"bernoulli"},"reward_policy":{"weights":{"latency":0,"success":1,"cache":0,"cost":0,"quality":0},"target_latency_ms":1,"max_latency_ms":2,"target_cost_usd":0,"max_cost_usd":1,"failure_is_zero":true},"warm_start":{"strategy":"cold"},"selection":{"selection":"nope"},"discount":null}`,
		`{"update_rule":{"rule":"bernoulli"},"reward_policy":{"weights":{"latency":0,"success":1,"cache":0,"cost":0,"quality":0},"target_latency_ms":1,"max_latency_ms":2,"target_cost_usd":0,"max_cost_usd":1,"failure_is_zero":true},"warm_start":{"strategy":"nope"},"selection":{"selection":"thompson"},"discount":null}`,
	}
	for i, bad := range bads {
		var cfg Config
		if err := json.Unmarshal([]byte(bad), &cfg); err == nil {
			t.Fatalf("unknown enum %d accepted", i)
		}
	}
}

// TestProtocolRustFixture reads the Rust-authored snapshot: arm set,
// posteriors, pulls, config, totals, and identity must all verify, and the
// snapshot must restore into a working policy.
func TestProtocolRustFixture(t *testing.T) {
	var snap Snapshot
	if err := json.Unmarshal(readFixture(t, "snapshot_rust.json"), &snap); err != nil {
		t.Fatalf("decode rust fixture: %v", err)
	}
	if snap.Version != SnapshotVersion {
		t.Fatalf("version=%d", snap.Version)
	}
	if snap.TotalPulls != 14 {
		t.Fatalf("totalPulls=%d want 14", snap.TotalPulls)
	}
	if snap.Config == nil {
		t.Fatal("config missing")
	}
	if snap.Config.Selection.Kind != PhasedSelection || snap.Config.Discount != 0 {
		t.Fatalf("config wrong: %+v", snap.Config)
	}
	if snap.Config.UpdateRule.Kind != Fractional {
		t.Fatalf("update rule wrong: %+v", snap.Config.UpdateRule)
	}
	if snap.Config.WarmStart.Kind != ColdStart {
		t.Fatalf("warm start wrong: %+v", snap.Config.WarmStart)
	}
	if len(snap.Arms) != 2 {
		t.Fatalf("arms=%d", len(snap.Arms))
	}
	byID := map[string]Arm{}
	for _, a := range snap.Arms {
		byID[a.ID] = a
	}
	gpt, ok := byID["openai/gpt-4"]
	if !ok || gpt.Posterior.Alpha != 7.5 || gpt.Posterior.Beta != 3.5 ||
		gpt.Posterior.Pulls != 9 || gpt.CumulativeReward != 6.5 || gpt.WarmStarted {
		t.Fatalf("gpt-4 wrong: %+v", gpt)
	}
	p, err := Restore(snap, DefaultConfig(), ExactSampler{})
	if err != nil {
		t.Fatalf("restore rust fixture: %v", err)
	}
	if p.TotalPulls() != 14 || p.SamplerName() != "exact" {
		t.Fatal("restored policy identity wrong")
	}
	if got := p.LoggingPolicyID(); got != "phased-v1" {
		t.Fatalf("logging id %q (phased config must not claim exact-thompson)", got)
	}
}

// TestProtocolLegacyGoFixture proves pre-canonical Go snapshots (PascalCase,
// no config) still decode, restoring under the caller-supplied config.
func TestProtocolLegacyGoFixture(t *testing.T) {
	var snap Snapshot
	if err := json.Unmarshal(readFixture(t, "legacy_go_snapshot.json"), &snap); err != nil {
		t.Fatalf("decode legacy fixture: %v", err)
	}
	if snap.Config != nil {
		t.Fatal("legacy fixture must have no config")
	}
	if len(snap.Arms) != 1 || snap.TotalPulls != 3 {
		t.Fatalf("arms/totals wrong: %+v", snap)
	}
	a := snap.Arms[0]
	if a.ID != "openai/gpt-4" || a.Posterior.Alpha != 3 || a.Posterior.Beta != 2 ||
		a.Posterior.Pulls != 3 || a.CumulativeReward != 2.5 {
		t.Fatalf("arm wrong: %+v", a)
	}
	p, err := Restore(snap, DefaultConfig(), ExactSampler{})
	if err != nil {
		t.Fatalf("restore legacy: %v", err)
	}
	if p.TotalPulls() != 3 {
		t.Fatal("restored pulls wrong")
	}
}

// TestProtocolInvalidVersionRejected pins version gating on the Go side
// (the Rust side mirrors it in tests/protocol.rs).
func TestProtocolInvalidVersionRejected(t *testing.T) {
	var snap Snapshot
	if err := json.Unmarshal(readFixture(t, "snapshot_rust.json"), &snap); err != nil {
		t.Fatal(err)
	}
	snap.Version = 999
	if _, err := Restore(snap, DefaultConfig(), ExactSampler{}); err == nil {
		t.Fatal("version 999 restored")
	}
}

// TestProtocolRewardVectors checks shared reward fixtures within 1e-12.
func TestProtocolRewardVectors(t *testing.T) {
	var rows []struct {
		LatencyMs float64  `json:"latency_ms"`
		Success   bool     `json:"success"`
		CacheHit  bool     `json:"cache_hit"`
		CostUSD   float64  `json:"cost_usd"`
		Quality   *float64 `json:"quality,omitempty"`
		Expected  float64  `json:"expected_total"`
	}
	if err := json.Unmarshal(readFixture(t, "rewards.json"), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("empty reward vectors")
	}
	rp := DefaultRewardPolicy()
	for i, r := range rows {
		o := Outcome{LatencyMs: r.LatencyMs, Success: r.Success, CacheHit: r.CacheHit, CostUSD: r.CostUSD}
		if r.Quality != nil {
			o.HasQuality = true
			o.Quality = *r.Quality
		}
		if got := rp.Reward(o); math.Abs(got-r.Expected) > 1e-12 {
			t.Fatalf("row %d: got %v want %v", i, got, r.Expected)
		}
	}
}

// TestProtocolSamplerMoments checks shared moment vectors with the
// implementation's own RNG: tolerance compatibility, never bitwise identity.
func TestProtocolSamplerMoments(t *testing.T) {
	var doc struct {
		Vectors []struct {
			Alpha  float64 `json:"alpha"`
			Beta   float64 `json:"beta"`
			N      int     `json:"n"`
			MeanLo float64 `json:"mean_lo"`
			MeanHi float64 `json:"mean_hi"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(readFixture(t, "sampler.json"), &doc); err != nil {
		t.Fatal(err)
	}
	for i, v := range doc.Vectors {
		rng := rand.New(rand.NewPCG(uint64(1000+i), 0))
		sum := 0.0
		for k := 0; k < v.N; k++ {
			sum += ExactSampler{}.Sample(rng, Posterior{Alpha: v.Alpha, Beta: v.Beta})
		}
		mean := sum / float64(v.N)
		if mean < v.MeanLo || mean > v.MeanHi {
			t.Fatalf("vector %d: mean %v outside [%v,%v]", i, mean, v.MeanLo, v.MeanHi)
		}
	}
}
