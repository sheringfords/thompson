package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// scenarioManifest builds a manifest from hand-specified jobs over the
// standard three treatments.
func scenarioManifest(t *testing.T, dir string, jobs []ManifestJob) string {
	t.Helper()
	m := &Manifest{
		ExperimentID: "scenario", WorkloadName: "scenario", Seed: 5,
		MaturationH: 24, Synthetic: true,
		Treatments: []TreatmentConfig{
			{ID: "t0", Arms: []string{"fixed"}, MaxAttempts: 1},
			{ID: "t1", Arms: []string{"cheap"}, MaxAttempts: 3},
			{ID: "t2", Arms: []string{"cheap", "strong"}, MaxAttempts: 3, Learn: true},
		},
		Jobs: jobs,
	}
	sum, err := m.contentHash()
	if err != nil {
		t.Fatal(err)
	}
	m.WorkloadVersion = sum
	path := dir + "/manifest.json"
	writeJSON(t, path, m)
	// Reload through validation (version check included).
	if _, err := LoadManifest(path); err != nil {
		t.Fatalf("scenario manifest invalid: %v", err)
	}
	return path
}

func sJob(id, strata string, behavior JobBehavior, arms map[string]ArmTruth) ManifestJob {
	return ManifestJob{JobID: id, Strata: strata, Arms: arms, Behavior: behavior}
}

func truthArm(p, cost, lat float64) ArmTruth {
	return ArmTruth{SuccessP: p, CostUSD: &cost, LatencyMs: lat}
}

func runScenario(t *testing.T, dir, mPath string) {
	t.Helper()
	r := openTestRunner(t, mPath, dir)
	defer r.Shutdown()
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func latestOutcome(t *testing.T, dir, tx, job string) outcome.OutcomeEvent {
	t.Helper()
	evs := outcomeVersions(t, dir, tx, job)
	best := 0
	for i := range evs {
		if evs[i].Version > evs[best].Version {
			best = i
		}
	}
	return evs[best]
}

func outcomeVersions(t *testing.T, dir, tx, job string) []outcome.OutcomeEvent {
	t.Helper()
	_, evs, err := harness.LoadTreatmentDir(dir + "/" + tx)
	if err != nil {
		t.Fatal(err)
	}
	jm, err := harness.LoadJobMap(dir + "/" + tx)
	if err != nil {
		t.Fatal(err)
	}
	// Join manifest job → gateway binding; fall back to identity when no
	// jobmap exists (hand-built ledgers) or the job settled unmapped.
	want := job
	if len(jm) > 0 {
		if mapped, ok := jm[job]; ok {
			want = mapped
		} else {
			found := false
			for _, ev := range evs {
				if ev.JobID == job {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("no outcome for %s/%s", tx, job)
			}
		}
	}
	var out []outcome.OutcomeEvent
	for _, ev := range evs {
		if ev.JobID == want {
			out = append(out, ev)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no outcome for %s/%s", tx, job)
	}
	return out
}

// Scenario 4: HTTP 200 with task-invalid output is REJECTED when the
// independent verifier rejects it — while transport success stays recorded.
func TestScenarioInvalidOutputRejected(t *testing.T) {
	dir := t.TempDir()
	arms := map[string]ArmTruth{"fixed": truthArm(1.0, 0.01, 100)}
	job := sJob("jinv", "s", BehaviorInvalidOutput, arms)
	job.Eligible = []string{"t0"}
	mPath := scenarioManifest(t, dir, []ManifestJob{job})
	runScenario(t, dir, mPath)

	ev := latestOutcome(t, dir, "t0", "jinv")
	if ev.Status != outcome.StatusRejected {
		t.Fatalf("status=%s want REJECTED", ev.Status)
	}
	// Transport success is preserved in evidence (the separation, from files).
	raw, err := os.ReadFile(dir + "/t0/evidence.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["event_type"] == "ExecutionObserved" && rec["success"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("no transport-success observation preserved alongside task rejection")
	}
}

// Scenario 5: timeout → UNKNOWN → late ACCEPTED learns exactly once.
func TestScenarioTimeoutThenAccept(t *testing.T) {
	dir := t.TempDir()
	arms := map[string]ArmTruth{"cheap": truthArm(0.9, 0.01, 100), "strong": truthArm(0.9, 0.02, 100)}
	job := sJob("jto", "s", BehaviorTimeoutThenAccept, arms)
	job.Eligible = []string{"t2"}
	mPath := scenarioManifest(t, dir, []ManifestJob{job})
	runScenario(t, dir, mPath)

	evs := outcomeVersions(t, dir, "t2", "jto")
	if len(evs) != 2 || evs[0].Status != outcome.StatusUnknown || evs[1].Status != outcome.StatusAccepted {
		t.Fatalf("versions wrong: %+v", evs)
	}
	// Timeout attempt carries unknown cost, never zero-filled.
	if evs[0].Attempts[0].CostUSD != nil {
		t.Fatal("timeout cost imputed")
	}
}

// Scenario 6: fallback chain preserves every attempt and cost; human
// correction is not model success (single version, no arm learning for the
// human step).
func TestScenarioFallbackChain(t *testing.T) {
	dir := t.TempDir()
	arms := map[string]ArmTruth{
		"cheap":  truthArm(0.0, 0.01, 100),
		"strong": truthArm(0.0, 0.02, 100),
	}
	job := sJob("jfb", "s", BehaviorNormal, arms)
	job.Eligible = []string{"t2"}
	job.Human = HumanTruth{Enabled: true, CostUSD: 2.5, LatencyMs: 600000, AlwaysSucceed: true}
	mPath := scenarioManifest(t, dir, []ManifestJob{job})
	runScenario(t, dir, mPath)

	ev := latestOutcome(t, dir, "t2", "jfb")
	if ev.Status != outcome.StatusAccepted {
		t.Fatalf("status=%s", ev.Status)
	}
	if len(ev.Attempts) < 2 {
		t.Fatalf("attempts=%d want fallback chain", len(ev.Attempts))
	}
	if ev.HumanReviewCostUSD == nil || *ev.HumanReviewCostUSD != 2.5 {
		t.Fatalf("human cost wrong: %+v", ev.HumanReviewCostUSD)
	}
	// Failed model attempts keep failure verdicts.
	for _, a := range ev.Attempts {
		if a.ExecutorID != "human-pool" && a.Verified != outcome.VerifiedFailure {
			t.Fatalf("model attempt verdict rewritten: %+v", a)
		}
	}
}

// Scenario 7: late correction settles v1+v2 with a correct version chain.
func TestScenarioCorrectionChain(t *testing.T) {
	dir := t.TempDir()
	arms := map[string]ArmTruth{"cheap": truthArm(1.0, 0.01, 100)}
	job := sJob("jcor", "s", BehaviorCorrectToReject, arms)
	job.Eligible = []string{"t2"}
	mPath := scenarioManifest(t, dir, []ManifestJob{job})
	runScenario(t, dir, mPath)

	evs := outcomeVersions(t, dir, "t2", "jcor")
	if len(evs) != 2 || evs[0].Status != outcome.StatusAccepted || evs[1].Status != outcome.StatusRejected {
		t.Fatalf("correction chain wrong: %+v", evs)
	}
	if evs[1].Supersedes != 1 {
		t.Fatal("correction does not supersede v1")
	}
}

// Scenario 8: missing settlement leaves a visibly unresolved job.
func TestScenarioUnresolvedVisible(t *testing.T) {
	dir := t.TempDir()
	arms := map[string]ArmTruth{"cheap": truthArm(1.0, 0.01, 100)}
	job := sJob("jun", "s", BehaviorUnresolved, arms)
	job.Eligible = []string{"t0"}
	mPath := scenarioManifest(t, dir, []ManifestJob{job})
	runScenario(t, dir, mPath)

	// No outcome row references this job under either key space.
	_, evs, err := harness.LoadTreatmentDir(dir + "/t0")
	if err != nil {
		t.Fatal(err)
	}
	jm, err := harness.LoadJobMap(dir + "/t0")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.JobID == "jun" || ev.JobID == jm["jun"] {
			t.Fatalf("unresolved job fabricated an outcome: %+v", ev)
		}
	}
	prior, err := LoadProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := prior["jun"]
	if !ok || row.Terminal {
		t.Fatalf("unresolved job not visibly pending: %+v", row)
	}
	// The report counts it unresolved, never failed.
	var allAssign []harness.Assignment
	var allEvents []outcome.OutcomeEvent
	for _, tx := range []string{"t0", "t1", "t2"} {
		as, evs, err := harness.LoadTreatmentDir(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		allAssign = append(allAssign, as...)
		allEvents = append(allEvents, evs...)
	}
	jobMaps := map[string]harness.JobMap{}
	for _, tx := range []string{"t0", "t1", "t2"} {
		jm, err := harness.LoadJobMap(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		jobMaps[tx] = jm
	}
	rep, err := harness.BuildReport(allAssign, allEvents, []string{"t0", "t1", "t2"}, "t0", "t2", []string{"t1"}, harness.ReportConfig{
		Maturation: 24 * time.Hour, Now: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		MinJobs: 1, CensorGate: 1, QualityFloor: 0, MinEffect: 0.15, BootstrapN: 50, BootstrapSeed: 1,
	}, jobMaps)
	if err != nil {
		t.Fatal(err)
	}
	// Unresolved jobs appear in accounting (assigned) without outcomes.
	total := 0
	for _, st := range rep.Treatments {
		total += st.Matured
	}
	if total != 1 {
		t.Fatalf("matured=%d want 1 (the unresolved job, accounted)", total)
	}
}
