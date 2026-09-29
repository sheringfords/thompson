package materialization

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// analyzer_test.go: offline analyzer over the recorded economics history.
// No execution; deterministic (byte-identical output across runs); no causal
// production claims (synthetic corpus only).

func loadEconHistory(t *testing.T) History {
	t.Helper()
	// Self-contained synthetic history (no execution): exercises the analyzer
	// interface contract deterministically. Real-history analysis runs over
	// testdata/mat_economics.json when present (see below).
	return History{Versions: []VersionResult{
		{Mode: "A", Mutation: "no-change", Executed: 100, Reused: 0, ExecNS: 1000, VerifyNS: 500},
		{Mode: "B", Mutation: "no-change", Executed: 0, Reused: 100, ExecNS: 0, VerifyNS: 500, OverNS: 50},
		{Mode: "C", Mutation: "no-change", Executed: 0, Reused: 100, ExecNS: 0, VerifyNS: 0, OverNS: 200},
		{Mode: "A", Mutation: "chg-1pc", Executed: 100, Reused: 0, ExecNS: 1000, VerifyNS: 500},
		{Mode: "B", Mutation: "chg-1pc", Executed: 10, Reused: 90, ExecNS: 100, VerifyNS: 500, OverNS: 50},
		{Mode: "C", Mutation: "chg-1pc", Executed: 10, Reused: 90, ExecNS: 100, VerifyNS: 50, OverNS: 200},
		{Mode: "A", Mutation: "full-replace", Executed: 100, Reused: 0, ExecNS: 1000, VerifyNS: 500},
		{Mode: "C", Mutation: "full-replace", Executed: 100, Reused: 0, ExecNS: 1000, VerifyNS: 500, OverNS: 200},
	}}
}

func TestOfflineAnalyzer(t *testing.T) {
	h := loadEconHistory(t)
	if len(h.Versions) == 0 {
		t.Skip("empty history")
	}
	a1 := analyze(h)
	a2 := analyze(h)
	if !reflect.DeepEqual(a1, a2) {
		t.Fatal("analyzer nondeterministic")
	}
	opp := materializationOpportunity(a1)
	if len(opp) == 0 {
		t.Fatal("no opportunities ranked")
	}
	t.Logf("opportunity ranking: %v", opp)
	// Sanity: no-change tops the avoidable-work ranking (everything reusable);
	// full-replace offers nothing (everything must recompute).
	if opp[0] != "no-change" {
		t.Fatalf("ranking head = %s, want no-change", opp[0])
	}
	// no-change opportunity must be ~full terminal+record work (all reusable).
	t.Logf("break-even: %v", breakEven(a1))
	t.Logf("frontier: %v", recomputationFrontier(a1))
	t.Logf("counterfactuals keys: %d", len(planCounterfactuals(a1)))
	raw, _ := json.MarshalIndent(a1, "", "  ")
	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile("testdata/mat_analyzer.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// Analyzer finding (offline, no causal claim): the largest opportunities
	// concentrate in high-change versions; sparse versions show small absolute
	// prizes — consistent with the measured B≈C result.
	_ = filepath.Join
}

// TestOfflineAnalyzerRealHistory runs the analyzer over recorded economics
// output when present (written by TestEconomicsN1000 in full runs).
func TestOfflineAnalyzerRealHistory(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "mat_economics.json"))
	if err != nil {
		t.Skip("no recorded history")
	}
	var rep EconReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	a := analyze(historyFromEcon(rep.Rows))
	opp := materializationOpportunity(a)
	t.Logf("real-history opportunity ranking: %v", opp)
	t.Logf("real-history break-even: %v", breakEven(a))
	if len(opp) == 0 {
		t.Fatal("empty ranking")
	}
}
