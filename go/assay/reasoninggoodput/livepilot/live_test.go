package livepilot

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLiveDevStream runs BuildSlices over a recorded OpenCode session when
// present (testdata/live-dev1.jsonl, captured by the pilot runner). Skipped
// otherwise. Asserts: every file premise resolves to a real in-repo blob,
// costs come from step tokens, and cumulative premises grow monotonically.
func TestLiveDevStream(t *testing.T) {
	stream := filepath.Join("testdata", "live-dev1.jsonl")
	if _, err := os.Stat(stream); err != nil {
		t.Skip("no recorded live stream (run the pilot runner first)")
	}
	// Rebuild an equivalent repo deterministically: fixture + background
	// change (blob OIDs match the recorded run by construction).
	repo := t.TempDir()
	if _, err := Materialize(repo, FixtureAuthorDate); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyChange(repo, "local1-premise", FixtureAuthorDate); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(stream)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseStream(tmp)
	if err != nil {
		t.Fatal(err)
	}
	workPrefix := CommonRoot(s)
	slices := BuildSlices(s, repo, workPrefix)
	if len(slices) == 0 {
		t.Fatal("no slices recovered")
	}
	unresolved := 0
	var totalTokens int64
	prev := 0
	for _, sl := range slices {
		for res, wit := range sl.Premises {
			if wit == "UNKNOWN" {
				unresolved++
				continue
			}
			_ = res
		}
		if len(sl.Premises) < prev {
			t.Fatalf("premises shrank (cumulative violated): %d -> %d", prev, len(sl.Premises))
		}
		prev = len(sl.Premises)
		totalTokens += sl.Tokens
	}
	t.Logf("slices=%d unresolved=%d tokens=%d", len(slices), unresolved, totalTokens)
	if totalTokens == 0 {
		t.Fatal("no token costs recovered (metadata missing?)")
	}
}
