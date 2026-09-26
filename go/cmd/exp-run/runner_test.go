package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// D1: resume binds to experiment, workload, seed, charter, treatments, and
// clock. Any mismatch refuses instead of continuing a foreign ledger.
func TestResumeBindingRefusesMismatch(t *testing.T) {
	mk := func(mut func(*Manifest)) *Manifest {
		m := generateManifest(77, 3)
		mut(m)
		sum, err := m.contentHash()
		if err != nil {
			t.Fatal(err)
		}
		m.WorkloadVersion = sum
		return m
	}
	base := mk(func(m *Manifest) {})
	writeHeader := func(t *testing.T, dir string, m *Manifest) {
		t.Helper()
		r, err := OpenRunner(RunnerConfig{Manifest: m, Root: dir,
			RouterBin: "unused", PubPorts: []int{1, 2, 3}, SettlePorts: []int{4, 5, 6},
			T0Clock: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), Step: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Shutdown()
		if err := r.checkOrWriteHeader(); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(*Manifest){
		"experiment": func(m *Manifest) { m.ExperimentID = "other" },
		"seed":       func(m *Manifest) { m.Seed = 999 },
		"charter":    func(m *Manifest) { m.CharterDigest = "other-charter" },
		"treatments": func(m *Manifest) { m.Treatments[0].MaxAttempts = 9 },
		"jobs":       func(m *Manifest) { m.Jobs = append(m.Jobs, m.Jobs[0]) },
	}
	for name, mut := range cases {
		dir := t.TempDir()
		writeHeader(t, dir, base)
		other := mk(mut)
		r, err := OpenRunner(RunnerConfig{Manifest: other, Root: dir,
			RouterBin: "unused", PubPorts: []int{1, 2, 3}, SettlePorts: []int{4, 5, 6},
			T0Clock: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), Step: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.checkOrWriteHeader(); err == nil {
			t.Fatalf("%s: incompatible resume accepted", name)
		}
		r.Shutdown()
	}
	// Identical manifest resumes cleanly (header already present).
	dir := t.TempDir()
	writeHeader(t, dir, base)
	r, err := OpenRunner(RunnerConfig{Manifest: base, Root: dir,
		RouterBin: "unused", PubPorts: []int{1, 2, 3}, SettlePorts: []int{4, 5, 6},
		T0Clock: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), Step: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()
	if err := r.checkOrWriteHeader(); err != nil {
		t.Fatalf("compatible resume refused: %v", err)
	}
}

// D1: late versions regenerate deterministically (the crash-safe queue is
// the verifier, not memory): same inputs, byte-identical version series.
func TestLateVersionsDeterministic(t *testing.T) {
	job := ManifestJob{JobID: "det", Strata: "s", Behavior: BehaviorTimeoutThenAccept,
		Arms: map[string]ArmTruth{"a": {SuccessP: 0.9, LatencyMs: 100}}}
	attempts := []outcome.Attempt{{
		AttemptID: "det-a0", Seq: 0, ExecutorID: "a", ArmID: "a",
		Transport: outcome.TransportTimeout, LatencyMs: 500,
		Validation: outcome.ValidationNotRun, Verified: outcome.VerifiedUnknown,
	}}
	v := FixtureVerifier{}
	a, okA := v.PlanSettlement(job, "exp", "dec1", "job-dec1", attempts, vAt)
	b, okB := v.PlanSettlement(job, "exp", "dec1", "job-dec1", attempts, vAt)
	if !okA || !okB || len(a) != 2 || len(b) != 2 {
		t.Fatalf("late plan wrong: %+v %v", a, okA)
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Fatal("late versions not deterministic")
	}
}
