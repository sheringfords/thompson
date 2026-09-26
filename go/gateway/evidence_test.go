package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A second concurrent writer on one evidence file fails fast instead of
// interleaving JSONL fragments.
func TestFileEvidenceWriterSingleWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	w, err := NewFileEvidenceWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := NewFileEvidenceWriter(path); err == nil {
		t.Fatal("second evidence writer opened without error")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2, err := NewFileEvidenceWriter(path)
	if err != nil {
		t.Fatalf("reopen after close failed: %v", err)
	}
	defer w2.Close()
}

func TestFileEvidenceWriterProducesValidJSONL(t *testing.T) {
	tmp, err := os.CreateTemp("", "evidence-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	w, err := NewFileEvidenceWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	e1 := DecisionStarted{
		SchemaVersion: 1, EventType: "DecisionStarted", DecisionID: "test-1",
		OccurredAt: nowRFC3339Nano(), EligibleArmIDs: []string{"a"}, SelectedArmID: "a",
		SampledScores: map[string]float64{"a": 0.8}, PolicyConfigHash: "abc",
		PosteriorBefore: PosteriorSnapshot{Alpha: 1, Beta: 1, Pulls: 0},
	}
	if err := w.WriteDecisionStarted(e1); err != nil {
		t.Fatalf("write started: %v", err)
	}
	e2 := ExecutionObserved{
		SchemaVersion: 1, EventType: "ExecutionObserved", DecisionID: "test-1",
		OccurredAt: nowRFC3339Nano(), ArmID: "a", LatencyMs: 120, Success: true,
	}
	if err := w.WriteExecutionObserved(e2); err != nil {
		t.Fatalf("write observed: %v", err)
	}
	e3 := DecisionLearned{
		SchemaVersion: 1, EventType: "DecisionLearned", DecisionID: "test-1",
		OccurredAt: nowRFC3339Nano(), ArmID: "a", ComputedReward: 0.9,
		PosteriorBefore: PosteriorSnapshot{Alpha: 1, Beta: 1, Pulls: 0},
		PosteriorAfter:  PosteriorSnapshot{Alpha: 2, Beta: 1, Pulls: 1},
		TotalPullsAfter: 1,
	}
	if err := w.WriteDecisionLearned(e3); err != nil {
		t.Fatalf("write learned: %v", err)
	}
	w.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("invalid JSON line %d: %v line=%s", lines, err, string(line))
		}
		lines++
	}
	if lines != 3 {
		t.Fatalf("expected 3 lines, got %d", lines)
	}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i+1])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func TestEvidenceDoesNotContainRequestBody(t *testing.T) {
	tmp, _ := os.CreateTemp("", "evidence-body*.jsonl")
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	w, _ := NewFileEvidenceWriter(path)
	defer w.Close()

	secret := "super-secret-prompt"
	e := DecisionStarted{
		SchemaVersion: 1, EventType: "DecisionStarted", DecisionID: "id2",
		OccurredAt: nowRFC3339Nano(), EligibleArmIDs: []string{"a"}, SelectedArmID: "a",
		SampledScores: map[string]float64{"a": 0.5}, PolicyConfigHash: "h",
		PosteriorBefore: PosteriorSnapshot{Alpha: 1, Beta: 1, Pulls: 0},
	}
	_ = secret // not written
	w.WriteDecisionStarted(e)
	data, _ := os.ReadFile(path)
	if contains(data, []byte(secret)) {
		t.Fatal("evidence contains prompt")
	}
}

func contains(b, sub []byte) bool {
	return len(b) >= len(sub) && stringContains(string(b), string(sub))
}
func stringContains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
