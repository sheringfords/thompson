package reuse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// comparison_test.go: Phase 4 three-mode comparison on the frozen matrix,
// the zero-false-reuse hard gate (dev + held-out), and machine-readable output.

// ComparisonReport is the raw machine-readable benchmark output.
type ComparisonReport struct {
	Modes []ModeMetrics `json:"modes"`
}

func runAllModes(t *testing.T, seeds []uint64) []ModeMetrics {
	t.Helper()
	var out []ModeMetrics
	for _, wl := range []string{WorkloadW1, WorkloadW2} {
		for _, mode := range []string{ModeB0, ModeB1, ModeB2} {
			s, err := Open(filepath.Join(t.TempDir(), mode+"-"+wl+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			r := NewRunner(s)
			m := r.RunMode(mode, Matrix([]string{wl}, seeds))
			out = append(out, *m)
			_ = s.Close()
		}
	}
	return out
}

func writeReport(t *testing.T, name string, modes []ModeMetrics) {
	t.Helper()
	raw, err := json.MarshalIndent(ComparisonReport{Modes: modes}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := "testdata"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestThreeModeComparisonDev(t *testing.T) {
	modes := runAllModes(t, DevSeeds)
	writeReport(t, "reuse_dev_results.json", modes)
	for _, m := range modes {
		t.Logf("%s/%s runs=%d hits=%d correct=%d false=%d badinv=%d fresh=%d avoided=%d saved=$%.2f spent=$%.2f overhead=%dns wall=%dms",
			m.Mode, m.Workload, m.Runs, m.Hits, m.CorrectReuse, m.FalseReuse,
			m.IncorrectInvalidate, m.FreshExecutions, m.FreshAvoided,
			m.CostAvoidedUSD, m.CostSpentUSD, m.VerifyOverheadNS, m.WallNS/1e6)
		for _, c := range m.FalseReuseCases {
			t.Logf("  FALSE-REUSE %s/%s: %s", m.Mode, m.Workload, c)
		}
	}
	// B1 must demonstrate the naive-baseline failure: keyed by input only, it
	// falsely reuses across toolchain/config/verifier/correction changes.
	b1false := 0
	for _, m := range modes {
		if m.Mode == ModeB1 {
			b1false += m.FalseReuse
		}
	}
	if b1false == 0 {
		t.Fatal("B1 naive baseline shows zero false reuse: matrix fails to discriminate")
	}
	// B2 hard gate on dev corpus: zero false reuse, zero incorrect invalidation.
	for _, m := range modes {
		if m.Mode != ModeB2 {
			continue
		}
		if m.FalseReuse != 0 {
			t.Fatalf("B2/%s FALSE REUSE: %v", m.Workload, m.FalseReuseCases)
		}
		if m.IncorrectInvalidate != 0 {
			t.Fatalf("B2/%s incorrect invalidation: %v", m.Workload, m.IncorrectInvCases)
		}
		if m.CorrectReuse == 0 {
			t.Fatalf("B2/%s reused nothing: no economic signal", m.Workload)
		}
	}
}

// TestZeroFalseReuseHeldOut is the Phase 4 hard gate on reserved seeds the
// prototype never developed against.
func TestZeroFalseReuseHeldOut(t *testing.T) {
	modes := runAllModes(t, HeldOutSeeds)
	writeReport(t, "reuse_heldout_results.json", modes)
	totalFalse, totalCorrect, totalBadInv := 0, 0, 0
	for _, m := range modes {
		if m.Mode != ModeB2 {
			continue
		}
		totalFalse += m.FalseReuse
		totalCorrect += m.CorrectReuse
		totalBadInv += m.IncorrectInvalidate
		for _, c := range m.FalseReuseCases {
			t.Errorf("HELD-OUT FALSE REUSE %s: %s", m.Workload, c)
		}
		for _, c := range m.IncorrectInvCases {
			t.Errorf("HELD-OUT BAD INVALIDATION %s: %s", m.Workload, c)
		}
	}
	t.Logf("held-out B2: correct=%d false=%d badinv=%d", totalCorrect, totalFalse, totalBadInv)
	if totalFalse != 0 {
		t.Fatalf("HARD GATE FAILED: %d held-out false reuses", totalFalse)
	}
	if totalBadInv != 0 {
		t.Fatalf("HARD GATE FAILED: %d held-out incorrect invalidations", totalBadInv)
	}
}
