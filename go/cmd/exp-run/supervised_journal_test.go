package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/gateway/journalstore"
	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// variantResult is everything comparable from one backend run.
type variantResult struct {
	assignments []harness.Assignment
	events      []outcome.OutcomeEvent
	decisions   map[string]string // gateway job -> selected arm
	safety      []string          // safety event types in order
}

func runT3Variant(t *testing.T, backend string) (string, *variantResult) {
	t.Helper()
	dir := t.TempDir()
	m := generateSupervisedManifest(20260707, 40)
	m.Treatments = []TreatmentConfig{
		{ID: "t3", Description: "supervised cost-aware Thompson", Arms: []string{"cheap", "strong"}, MaxAttempts: 3, Learn: true, CostAware: true, StorageBackend: backend},
	}
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)
	pubBase, settleBase := nextPortBases()
	scb, err := json.Marshal(supervisedSafetyFor([]string{"cheap", "strong"}, "strong"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenRunner(RunnerConfig{
		Manifest: m, Root: dir, RouterBin: routerBin(t),
		PubPorts: []int{pubBase}, SettlePorts: []int{settleBase}, Token: "e2e-token",
		Timeout: 20 * time.Second,
		T0Clock: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), Step: 60 * time.Second,
		SelectionSeed: 777, SafetyConfigs: map[string][]byte{"t3": scb}, OperatorToken: "op",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()
	if err := r.Boot(); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res := &variantResult{decisions: map[string]string{}}
	asg, _, err := harness.LoadTreatmentDir(filepath.Join(dir, "t3"))
	if err != nil {
		t.Fatal(err)
	}
	res.assignments = asg
	if backend == "journal" {
		be, err := journalstore.OpenBackend(filepath.Join(dir, "t3"), "journal.db")
		if err != nil {
			t.Fatal(err)
		}
		defer be.Close()
		res.events = be.Outcomes().Events()
		if err := be.Decisions().Scan(func(d gateway.CommittedDecision) bool {
			res.decisions[d.JobID] = d.SelectedArmID
			return true
		}); err != nil {
			t.Fatal(err)
		}
		sevs, err := be.Safety().Events()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range sevs {
			res.safety = append(res.safety, e.Type+":"+e.Arm)
		}
		// No JSONL authorities may exist beside the journal.
		for _, f := range []string{"decisions.jsonl", "outcomes.jsonl", "safety.jsonl"} {
			if _, err := statPath(filepath.Join(dir, "t3", f)); err == nil {
				t.Fatalf("journal run created competing %s", f)
			}
		}
	} else {
		_, evs, err := harness.LoadTreatmentDir(filepath.Join(dir, "t3"))
		if err != nil {
			t.Fatal(err)
		}
		res.events = evs
		for _, d := range readDecisions(t, filepath.Join(dir, "t3", "decisions.jsonl")) {
			res.decisions[d.JobID] = d.SelectedArmID
		}
		for _, e := range readSafetyEvents(t, filepath.Join(dir, "t3", "safety.jsonl")) {
			res.safety = append(res.safety, e.Type+":"+e.Arm)
		}
		// No journal database may exist beside JSONL.
		if _, err := statPath(filepath.Join(dir, "t3", "journal.db")); err == nil {
			t.Fatal("jsonl run created a journal database")
		}
	}
	return dir, res
}

// Journal-vs-JSONL equivalence on one frozen workload through real
// binaries. Assignments use manifest IDs (directly comparable); decisions
// and outcomes use gateway-random IDs, so both join through the jobmap to
// manifest IDs before comparison. Only storage-specific metadata may
// differ — never decisions, settlements, safety state, or verdicts.
func TestJournalEquivalence(t *testing.T) {
	var jsonlDir, journalDir string
	var jsonlRes, journalRes *variantResult
	t.Run("jsonl", func(t *testing.T) { jsonlDir, jsonlRes = runT3Variant(t, "") })
	t.Run("journal", func(t *testing.T) { journalDir, journalRes = runT3Variant(t, "journal") })
	if len(jsonlRes.assignments) != len(journalRes.assignments) {
		t.Fatalf("assignment counts differ: %d vs %d", len(jsonlRes.assignments), len(journalRes.assignments))
	}
	for i := range jsonlRes.assignments {
		a, b := jsonlRes.assignments[i], journalRes.assignments[i]
		if a.JobID != b.JobID || a.Treatment != b.Treatment {
			t.Fatalf("assignment %d differs: %+v vs %+v", i, a, b)
		}
	}
	armOf := func(t *testing.T, dir string, res *variantResult) map[string]string {
		t.Helper()
		jm, err := harness.LoadJobMap(filepath.Join(dir, "t3"))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for manifestJob, gatewayJob := range jm {
			arm, ok := res.decisions[gatewayJob]
			if !ok {
				t.Fatalf("manifest job %s maps to gateway job %s with no decision", manifestJob, gatewayJob)
			}
			out[manifestJob] = arm
		}
		return out
	}
	jobsJ := armOf(t, jsonlDir, jsonlRes)
	jobsK := armOf(t, journalDir, journalRes)
	if len(jobsJ) != len(jobsK) {
		t.Fatalf("decided manifest jobs differ: %d vs %d", len(jobsJ), len(jobsK))
	}
	for job, arm := range jobsJ {
		if jobsK[job] != arm {
			t.Fatalf("manifest job %s selected %s (jsonl) vs %s (journal)", job, arm, jobsK[job])
		}
	}
	verOf := func(t *testing.T, dir string, res *variantResult) map[string]uint64 {
		t.Helper()
		jm, err := harness.LoadJobMap(filepath.Join(dir, "t3"))
		if err != nil {
			t.Fatal(err)
		}
		rev := map[string]string{}
		for mj, gj := range jm {
			rev[gj] = mj
		}
		out := map[string]uint64{}
		for _, e := range res.events {
			mj, ok := rev[e.JobID]
			if !ok {
				continue
			}
			if e.Version > out[mj] {
				out[mj] = e.Version
			}
		}
		return out
	}
	jv, kv := verOf(t, jsonlDir, jsonlRes), verOf(t, journalDir, journalRes)
	if len(jv) != len(kv) {
		t.Fatalf("settled manifest jobs differ: %d vs %d", len(jv), len(kv))
	}
	for job, v := range jv {
		if kv[job] != v {
			t.Fatalf("manifest job %s outcome version %d vs %d", job, v, kv[job])
		}
	}
	if len(jsonlRes.safety) != len(journalRes.safety) {
		t.Fatalf("safety sequences differ: %v vs %v", jsonlRes.safety, journalRes.safety)
	}
	for i := range jsonlRes.safety {
		if jsonlRes.safety[i] != journalRes.safety[i] {
			t.Fatalf("safety %d differs: %s vs %s", i, jsonlRes.safety[i], journalRes.safety[i])
		}
	}
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC).Add(48 * time.Hour)
	repOf := func(dir string, res *variantResult) harness.Report {
		jm, err := harness.LoadJobMap(filepath.Join(dir, "t3"))
		if err != nil {
			t.Fatal(err)
		}
		return harness.Analyze(harness.MatureJobsMapped(res.assignments, res.events, now, 24*time.Hour, jm), harness.ReportConfig{
			Maturation: 24 * time.Hour, Now: now, MinJobs: 3, CensorGate: 0.9,
			QualityFloor: 0.3, MinEffect: 0.05, BootstrapN: 100, BootstrapSeed: 7,
			MaxUnmeteredShare: 0.30,
		}, "t3", "t3", nil, true, true)
	}
	rJ, rK := repOf(jsonlDir, jsonlRes), repOf(journalDir, journalRes)
	if rJ.Verdict != rK.Verdict {
		t.Fatalf("verdicts differ: %s vs %s", rJ.Verdict, rK.Verdict)
	}
	t.Logf("equivalent: %d jobs, verdict=%s", len(jsonlRes.assignments), rJ.Verdict)
}

func statPath(path string) (os.FileInfo, error) { return os.Stat(path) }

func readSafetyEvents(t *testing.T, path string) []gateway.SafetyEvent {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []gateway.SafetyEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev gateway.SafetyEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
