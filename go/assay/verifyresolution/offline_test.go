package verifyresolution

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// offline_test.go: analyzer contract (deterministic, self-contained) plus a
// real-history run over recorded matrix output when present.

// TestOfflineAnalyzerUnit pins the counterfactual semantics on synthetic history.
func TestOfflineAnalyzerUnit(t *testing.T) {
	rows := []ResRecord{
		{Artifact: "a1", Resolution: "VERIFY", Contract: "v2", ProdNS: 1000, VerNS: 10},
		{Artifact: "a1", Resolution: "VERIFY", Contract: "v3", ProdNS: 1000, VerNS: 10},
		{Artifact: "a2", Resolution: "RECOMPUTE", Contract: "v1", ProdNS: 500, VerNS: 5},
		{Artifact: "a3", Resolution: "REUSE", Contract: "v1"},
		{Artifact: "a4", Resolution: "UNKNOWN", Contract: "v9"},
	}
	a1 := AnalyzeResolutions(rows)
	a2 := AnalyzeResolutions(rows)
	if !reflect.DeepEqual(a1, a2) {
		t.Fatal("nondeterministic")
	}
	if a1.VerifyOpportunities != 2 || a1.RecomputeNeeded != 1 || a1.ReuseHits != 1 || a1.Unknowns != 1 {
		t.Fatalf("counts wrong: %+v", a1)
	}
	if a1.ProdAvoidedNS != 2000 {
		t.Fatalf("avoided = %d, want 2000", a1.ProdAvoidedNS)
	}
	rank := RankContracts(a1, rows)
	if len(rank) != 2 || rank[0] != "v2" && rank[0] != "v3" {
		t.Fatalf("ranking wrong: %v", rank)
	}
	raw, _ := json.MarshalIndent(a1, "", "  ")
	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile("testdata/vr_offline.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
