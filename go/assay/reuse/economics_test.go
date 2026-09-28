package reuse

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// economics_test.go: Phase 7 economic and systems value measurements.
// Execution cost is MODELED (W1 $0.50, W2 $0.20 per fresh run); reuse-machinery
// cost is MEASURED (real ns for hashing, lookup, evaluation, traversal).
// Time→$ conversion uses a stated assumption, labeled ASSUMPTION wherever used.

// ComputeDollarPerNS prices compute at $0.05/hour (ASSUMPTION, stated).
const ComputeDollarPerNS = 0.05 / (3600.0 * 1e9)

// EconomicsReport is the machine-readable economics output.
type EconomicsReport struct {
	RepeatSweep   []RepeatRow   `json:"repeat_sweep"`
	OverheadNS    OverheadBreak `json:"overhead_ns"`
	Scaling       []ScalingRow  `json:"scaling"`
	Fanout        []FanoutRow   `json:"fanout"`
	ColdWarm      ColdWarm      `json:"cold_warm"`
	BreakEvenNote string        `json:"breakeven_note"`
}

type RepeatRow struct {
	Repeats     int     `json:"repeats"`
	B0CostUSD   float64 `json:"b0_modeled_usd"`
	B2CostUSD   float64 `json:"b2_modeled_plus_measured_usd"`
	SavedUSD    float64 `json:"saved_usd"`
	OverheadUSD float64 `json:"measured_overhead_usd"`
	NetWin      bool    `json:"net_win"`
}

type OverheadBreak struct {
	KeyBuildNS    float64 `json:"key_build_digest_ns"`
	LookupNS      float64 `json:"lookup_ns"`
	EvaluateNS    float64 `json:"evaluate_hit_ns"`
	BytesVerifyNS float64 `json:"bytes_verify_ns"`
	PerHitNS      float64 `json:"per_hit_total_ns"`
}

type ScalingRow struct {
	Records        int     `json:"records"`
	PublishMS      int64   `json:"publish_total_ms"`
	ColdLookupNS   float64 `json:"cold_lookup_ns"`
	WarmLookupNS   float64 `json:"warm_lookup_ns"`
	BytesPerRecord float64 `json:"bytes_per_record"`
	Note           string  `json:"note,omitempty"`
}

type FanoutRow struct {
	Shape       string  `json:"shape"`
	Edges       int     `json:"edges"`
	TotalNS     int64   `json:"total_ns"`
	PerEdgeNS   float64 `json:"per_edge_ns"`
	Invalidated int     `json:"invalidated"`
}

type ColdWarm struct {
	Records        int     `json:"records"`
	ReplayMS       int64   `json:"replay_ms"`
	ReplayPerRecNS float64 `json:"replay_per_record_ns"`
}

func benchOverhead(t *testing.T) OverheadBreak {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ov.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k := testKey()
	body := testBody("ov")
	bbytes := []byte("artifact-bytes-ov")
	pubBody := body
	pubBody.ArtifactDigest = DigestBytes(bbytes)
	if _, err := s.Publish(k, pubBody); err != nil {
		t.Fatal(err)
	}
	lookup := acceptLookup(pubBody.OutcomeJobID, 1)
	const N = 2000
	var kb, lk, ev, bv time.Duration
	live := liveOf(k)
	for i := 0; i < N; i++ {
		t0 := time.Now()
		c := k.Canonical()
		_ = DigestBytes(c)
		kb += time.Since(t0)
		t0 = time.Now()
		_, ok := s.Lookup(k.KeyDigest())
		if !ok {
			t.Fatal("lost record")
		}
		lk += time.Since(t0)
		t0 = time.Now()
		if d := s.Evaluate(k, live, lookup); d.Validity != ValidityValid {
			t.Fatalf("evaluate = %+v", d)
		}
		ev += time.Since(t0)
		t0 = time.Now()
		if err := s.VerifyBytes(k.KeyDigest(), bbytes); err != nil {
			t.Fatal(err)
		}
		bv += time.Since(t0)
	}
	mean := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / N }
	return OverheadBreak{
		KeyBuildNS: mean(kb), LookupNS: mean(lk), EvaluateNS: mean(ev),
		BytesVerifyNS: mean(bv), PerHitNS: mean(kb + lk + ev + bv),
	}
}

func TestEconomicsAndScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("economics assay needs full run")
	}
	rep := EconomicsReport{}
	rep.OverheadNS = benchOverhead(t)
	t.Logf("overhead/hit: key=%.0fns lookup=%.0fns eval=%.0fns verify=%.0fns total=%.0fns ($%.9f at ASSUMED $0.05/h)",
		rep.OverheadNS.KeyBuildNS, rep.OverheadNS.LookupNS, rep.OverheadNS.EvaluateNS,
		rep.OverheadNS.BytesVerifyNS, rep.OverheadNS.PerHitNS,
		rep.OverheadNS.PerHitNS*ComputeDollarPerNS)

	// Repeat-rate sweep on W1 ($0.50 modeled execution): B0 pays R executions,
	// B2 pays 1 + R measured overheads.
	for _, R := range []int{1, 2, 5, 20, 100} {
		exec := 0.50
		ov := (rep.OverheadNS.PerHitNS * float64(R)) * ComputeDollarPerNS
		b0 := float64(R) * exec
		b2 := exec + ov
		rep.RepeatSweep = append(rep.RepeatSweep, RepeatRow{
			Repeats: R, B0CostUSD: b0, B2CostUSD: b2,
			SavedUSD: b0 - b2, OverheadUSD: ov, NetWin: b2 < b0,
		})
	}

	// Record-count scaling: measured at 1k/10k (per-record fsync, as the
	// prototype commits). 100k/1M rows are PROJECTIONS from measured
	// bytes/record (storage is strictly linear) and measured O(1) warm-lookup
	// flatness — labeled as such, not silent extrapolation.
	for _, n := range []int{1000, 10000} {
		rep.Scaling = append(rep.Scaling, scalingPoint(t, n))
	}
	rep.Scaling = append(rep.Scaling, projectScaling(rep.Scaling))
	for _, r := range rep.Scaling {
		t.Logf("scaling n=%d publish=%dms cold=%.0fns warm=%.0fns B/rec=%.0f %s",
			r.Records, r.PublishMS, r.ColdLookupNS, r.WarmLookupNS, r.BytesPerRecord, r.Note)
	}

	// Fan-out: chain L=500 correction propagation; star 1->300 stale fan-out.
	rep.Fanout = append(rep.Fanout, fanoutChain(t, 500), fanoutStar(t, 300))
	for _, f := range rep.Fanout {
		t.Logf("fanout %s edges=%d total=%dms per-edge=%.0fns invalidated=%d",
			f.Shape, f.Edges, f.TotalNS/1e6, f.PerEdgeNS, f.Invalidated)
	}

	// Cold (replay from file) vs warm (map) at 10k.
	rep.ColdWarm = coldWarm(t, 10000)
	t.Logf("cold replay 10k: %dms (%.0fns/rec)", rep.ColdWarm.ReplayMS, rep.ColdWarm.ReplayPerRecNS)

	rep.BreakEvenNote = "Break-even at ASSUMED $0.05/compute-hour: reuse machinery (~" +
		fmt.Sprintf("%.0f", rep.OverheadNS.PerHitNS) + "ns/hit) costs ~$" +
		fmt.Sprintf("%.9f", rep.OverheadNS.PerHitNS*ComputeDollarPerNS) +
		"/hit; any execution costing more than ~2x that is net-positive from the second identical run. " +
		"At modeled W1/W2 costs ($0.50/$0.20) the machinery pays for itself at R>=2. " +
		"Negative case R=1 (no repeats): machinery is pure overhead (measured, reported, no savings claimed)."
	raw, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile("testdata/reuse_economics.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func scalingPoint(t *testing.T, n int) ScalingRow {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scale.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	for i := 0; i < n; i++ {
		k := testKey()
		k.Operation = fmt.Sprintf("w1.op-%d", i)
		k.InputDigest = DigestString(fmt.Sprintf("input-%d", i))
		b := PublishBody{
			ArtifactDigest: DigestString(fmt.Sprintf("art-%d", i)),
			Verification:   VerificationAccepted,
			EvidenceID:     fmt.Sprintf("ev-%d", i),
			ActualCostUSD:  0.5,
			ReceiptID:      fmt.Sprintf("rcpt-%d", i),
			OutcomeJobID:   fmt.Sprintf("job-%d", i%100),
			OutcomeVersion: 1,
		}
		if _, err := s.Publish(k, b); err != nil {
			t.Fatal(err)
		}
	}
	pubMS := time.Since(t0).Milliseconds()
	probe := testKey()
	probe.Operation = "w1.op-7"
	probe.InputDigest = DigestString("input-7")
	const M = 500
	t0 = time.Now()
	for i := 0; i < M; i++ {
		if _, ok := s.Lookup(probe.KeyDigest()); !ok {
			t.Fatal("lost probe record")
		}
	}
	warm := float64(time.Since(t0).Nanoseconds()) / M
	_ = s.Close()
	// Cold: reopen (replay) then lookup.
	t0 = time.Now()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	replayMS := time.Since(t0).Milliseconds()
	t1 := time.Now()
	for i := 0; i < M; i++ {
		if _, ok := s2.Lookup(probe.KeyDigest()); !ok {
			t.Fatal("lost probe record after reopen")
		}
	}
	cold := float64(time.Since(t1).Nanoseconds()) / M
	a, _, by := s2.GraphStats()
	_ = s2.Close()
	return ScalingRow{Records: a, PublishMS: pubMS, ColdLookupNS: cold,
		WarmLookupNS: warm, BytesPerRecord: float64(by) / float64(a),
		Note: fmt.Sprintf("replay %dms", replayMS)}
}

// projectScaling appends labeled 100k/1M PROJECTION rows from measured data.
func projectScaling(measured []ScalingRow) ScalingRow {
	last := measured[len(measured)-1]
	var warm, bpr float64
	for _, r := range measured {
		warm += r.WarmLookupNS
		bpr += r.BytesPerRecord
	}
	warm /= float64(len(measured))
	bpr /= float64(len(measured))
	_ = last
	return ScalingRow{Records: 1000000, WarmLookupNS: warm, ColdLookupNS: warm,
		BytesPerRecord: bpr,
		Note:           "PROJECTION from 1k/10k measured means: storage strictly linear (bytes/record stable), warm lookup O(1) map (flat 1k->10k); 100k build with per-record fsync not attempted in-test (see report for fsync analysis)"}
}

func fanoutChain(t *testing.T, L int) FanoutRow {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "chain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	keys := make([]ExecutionKey, L)
	bodies := make([]PublishBody, L)
	for i := 0; i < L; i++ {
		k := testKey()
		k.Operation = fmt.Sprintf("w1.step-%d", i)
		k.InputDigest = DigestString(fmt.Sprintf("chain-input-%d", i))
		if i > 0 {
			k.Deps = append(append([]Dep(nil), k.Deps...),
				Dep{Name: UpstreamPrefix + keys[i-1].KeyDigest(), Digest: bodies[i-1].ArtifactDigest})
		}
		b := testBody(fmt.Sprintf("chain-%d", i))
		b.OutcomeJobID = "job-chain-0"
		if _, err := s.Publish(k, b); err != nil {
			t.Fatal(err)
		}
		keys[i], bodies[i] = k, b
	}
	t0 := time.Now()
	n := s.PropagateCorrection("job-chain-0", 2, "REJECTED")
	dt := time.Since(t0)
	return FanoutRow{Shape: fmt.Sprintf("chain-L%d-correction", L), Edges: L - 1,
		TotalNS: dt.Nanoseconds(), PerEdgeNS: float64(dt.Nanoseconds()) / float64(L),
		Invalidated: n}
}

func fanoutStar(t *testing.T, F int) FanoutRow {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "star.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := testKey()
	rb := testBody("star-root")
	if _, err := s.Publish(root, rb); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < F; i++ {
		k := testKey()
		k.Operation = fmt.Sprintf("w1.leaf-%d", i)
		k.InputDigest = DigestString(fmt.Sprintf("leaf-%d", i))
		k.Deps = append(append([]Dep(nil), k.Deps...),
			Dep{Name: UpstreamPrefix + root.KeyDigest(), Digest: rb.ArtifactDigest})
		b := testBody(fmt.Sprintf("leaf-%d", i))
		b.OutcomeJobID = fmt.Sprintf("job-leaf-%d", i)
		if _, err := s.Publish(k, b); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now()
	n := s.PropagateStale(root.KeyDigest(), "config", "root dep changed")
	dt := time.Since(t0)
	return FanoutRow{Shape: fmt.Sprintf("star-F%d-stale", F), Edges: F,
		TotalNS: dt.Nanoseconds(), PerEdgeNS: float64(dt.Nanoseconds()) / float64(F+1),
		Invalidated: n}
}

func coldWarm(t *testing.T, n int) ColdWarm {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cw.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		k := testKey()
		k.Operation = fmt.Sprintf("w1.cw-%d", i)
		k.InputDigest = DigestString(fmt.Sprintf("cw-%d", i))
		b := testBody(fmt.Sprintf("cw-%d", i))
		if _, err := s.Publish(k, b); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	t0 := time.Now()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dt := time.Since(t0)
	defer s2.Close()
	return ColdWarm{Records: n, ReplayMS: dt.Milliseconds(),
		ReplayPerRecNS: float64(dt.Nanoseconds()) / float64(n)}
}
