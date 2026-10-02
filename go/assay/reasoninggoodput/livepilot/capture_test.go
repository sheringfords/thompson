package livepilot

import (
	"os"
	"path/filepath"
	"testing"
)

func writeStream(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stream.jsonl")
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseStream(t *testing.T) {
	p := writeStream(t,
		`{"type":"step_start","timestamp":1,"sessionID":"s1","part":{"type":"step-start"}}`,
		`{"type":"text","timestamp":2,"sessionID":"s1","part":{"type":"text","text":"hello","tool":""}}`,
		`{"type":"tool_use","timestamp":3,"sessionID":"s1","part":{"type":"tool","tool":"read","state":{"status":"completed","input":{"path":"calc/tax.go"},"output":"Read file x"}}}`,
		`{"type":"weird","timestamp":4,"sessionID":"s1","part":{"type":"mystery-box"}}`,
		`not json at all`,
	)
	s, err := ParseStream(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != "s1" {
		t.Fatalf("session %q", s.SessionID)
	}
	var kinds []string
	for _, part := range s.Parts {
		kinds = append(kinds, part.Type)
	}
	if len(kinds) != 4 {
		t.Fatalf("parts %v", kinds)
	}
	if len(s.Unknown) != 1 {
		t.Fatalf("unknown %v", s.Unknown)
	}
	reads := s.ReadEvents()
	if len(reads) != 1 || reads[0].Tool != "read" {
		t.Fatalf("reads %v", reads)
	}
	if n := s.ModelCalls(); n != 1 {
		t.Fatalf("model calls %d", n)
	}
}

func TestCumulativePremises(t *testing.T) {
	// Cumulative-union premise semantics (unit-level): slice 0 sees {a},
	// slice 1 sees {a,b}. SARF prices the narrow slice over total cost.
	// The full BuildSlices path (messageID grouping, content-derived OIDs,
	// token costs) is covered by TestLiveDevStream on a recorded stream.
	full := map[string]string{"a": "w", "b": "w"}
	calls := []CallSlice{
		{Index: 0, Premises: map[string]string{"a": "w"}, CostNS: 100},
		{Index: 1, Premises: map[string]string{"a": "w", "b": "w"}, CostNS: 300},
	}
	r := SARF(calls, full)
	if r.SARF != 0.25 {
		t.Fatalf("sarf %v (want 0.25)", r.SARF)
	}
	if r.MedianPremise != 2 {
		t.Fatalf("median %d (want 2)", r.MedianPremise)
	}
}

func TestSARFMath(t *testing.T) {
	full := map[string]string{"a": "w", "b": "w", "c": "w"}
	calls := []CallSlice{
		{Index: 0, Premises: map[string]string{"a": "w"}, CostNS: 100},
		{Index: 1, Premises: map[string]string{"a": "w", "b": "w", "c": "w"}, CostNS: 300},
	}
	r := SARF(calls, full)
	if r.SARF != 0.25 {
		t.Fatalf("sarf %v", r.SARF)
	}
	if r.MedianPremise != 3 {
		t.Fatalf("median %d", r.MedianPremise)
	}
}

func TestCounterfactualMath(t *testing.T) {
	calls := []CallSlice{
		{Index: 0, Premises: map[string]string{"a": "w"}, CostNS: 100},
		{Index: 1, Premises: map[string]string{"a": "w", "b": "w"}, CostNS: 300},
	}
	l1, l2, d3, p3 := Counterfactual(calls, map[string]bool{"b": true})
	if l1 != 400 || l2 != 400 || d3 != 300 || p3 != 100 {
		t.Fatalf("got %d %d %d %d", l1, l2, d3, p3)
	}
	l1, l2, d3, p3 = Counterfactual(calls, map[string]bool{})
	if l1 != 0 || l2 != 0 || d3 != 0 || p3 != 0 {
		t.Fatalf("clean run must zero counterfactuals: %d %d %d %d", l1, l2, d3, p3)
	}
}
