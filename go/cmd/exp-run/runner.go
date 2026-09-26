package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// ProgressPhase tracks one job's durable progress. The log is append-only;
// loaders take the last line per job.
type ProgressPhase string

const (
	PhaseAssigned  ProgressPhase = "assigned"
	PhaseAttempted ProgressPhase = "attempted"
	PhaseSettledV1 ProgressPhase = "settled-v1"
	PhaseComplete  ProgressPhase = "complete"
)

// ProgressRow is one progress-log line.
type ProgressRow struct {
	JobID       string        `json:"job_id"`
	Treatment   string        `json:"treatment"`
	Probability float64       `json:"probability"`
	Phase       ProgressPhase `json:"phase"`
	Decisions   []string      `json:"decisions,omitempty"`
	Versions    []uint64      `json:"versions,omitempty"`
	Terminal    bool          `json:"terminal"`
}

// RunnerConfig wires a manifest to gateway processes.
type RunnerConfig struct {
	Manifest    *Manifest
	Root        string
	RouterBin   string
	T0          string // treatment IDs in manifest order
	PubPorts    []int
	SettlePorts []int
	Token       string
	Timeout     time.Duration
	T0Clock     time.Time
	Step        time.Duration
	// CrashAfter exits the process after N jobs (deterministic crash
	// injection for resume proofs; 0 disables).
	CrashAfter int
	// SelectionSeed, when nonzero, seeds every treatment gateway's request
	// RNG identically per treatment (base+index). Required for reproducible
	// dry runs; production omits it (time-seeded).
	SelectionSeed uint64
}

// Runner executes the experiment: assign → execute → verify → settle, with
// resume from the progress log. It is strictly sequential: one job at a
// time, which makes ledger reads unambiguous and RNG consumption irrelevant
// (all randomness is hash-derived per job/attempt).
type Runner struct {
	cfg      RunnerConfig
	assigner harness.Assigner
	gateways map[string]*GatewayProc
	verifier Verifier
	progress *os.File
	late     []lateSettlement
	doneJobs int
	// assigned tracks persisted (treatment, job) assignment rows so resume
	// never double-appends them.
	assigned map[string]bool
}

type lateSettlement struct {
	tx          string
	manifestJob string
	ev          outcome.OutcomeEvent
}

// OpenRunner validates the manifest against treatments and opens the
// progress log (creating the root if needed).
func OpenRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Manifest == nil {
		return nil, fmt.Errorf("exp-run: nil manifest")
	}
	if len(cfg.PubPorts) != len(cfg.Manifest.Treatments) || len(cfg.SettlePorts) != len(cfg.Manifest.Treatments) {
		return nil, fmt.Errorf("exp-run: need one public+settle port per treatment")
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, err
	}
	txIDs := make([]string, len(cfg.Manifest.Treatments))
	weights := make([]float64, len(cfg.Manifest.Treatments))
	for i, t := range cfg.Manifest.Treatments {
		txIDs[i] = t.ID
		weights[i] = 1
	}
	pf, err := os.OpenFile(cfg.Root+"/progress.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Runner{
		cfg:      cfg,
		assigner: harness.Assigner{Seed: cfg.Manifest.Seed, Treatments: txIDs, Weights: weights},
		gateways: make(map[string]*GatewayProc),
		verifier: FixtureVerifier{},
		progress: pf,
		assigned: loadAssignedSet(cfg.Root, txIDs),
	}, nil
}

// recordJobMap persists the manifest→gateway join. Rows are append-only and
// LoadJobMap takes the latest per job, so a resumed re-execution (new
// decisions, new gateway binding) supersedes the orphaned first-run row.
func (r *Runner) recordJobMap(tx, manifestJob, gatewayJob string) error {
	f, err := os.OpenFile(r.cfg.Root+"/"+tx+"/jobmap.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(map[string]string{"manifest_job_id": manifestJob, "gateway_job_id": gatewayJob})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return nil
}

// loadAssignedSet indexes already-persisted assignment rows so resume adds
// none twice (double rows would double-count jobs in the report).
func loadAssignedSet(root string, txIDs []string) map[string]bool {
	out := map[string]bool{}
	for _, tx := range txIDs {
		rows, err := readAssignmentRows(root + "/" + tx + "/assignments.jsonl")
		if err != nil {
			continue
		}
		for _, job := range rows {
			out[tx+"\x00"+job] = true
		}
	}
	return out
}

func readAssignmentRows(path string) ([]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufioScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var a harness.Assignment
		if err := json.Unmarshal(line, &a); err != nil {
			return nil, err
		}
		out = append(out, a.JobID)
	}
	return out, sc.Err()
}

// Boot spawns one gateway binary per treatment with isolated files.
func (r *Runner) Boot() error {
	for i, t := range r.cfg.Manifest.Treatments {
		dir := r.cfg.Root + "/" + t.ID
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		arms := strings.Join(t.Arms, ",")
		mapper := ""
		if !t.Learn {
			mapper = "noop"
		}
		selSeed := ""
		if r.cfg.SelectionSeed != 0 {
			selSeed = strconv.FormatUint(r.cfg.SelectionSeed+uint64(i), 10)
		}
		g, err := SpawnGateway(r.cfg.RouterBin, t.ID, dir,
			"127.0.0.1:"+itoa(r.cfg.PubPorts[i]),
			"127.0.0.1:"+itoa(r.cfg.SettlePorts[i]),
			r.cfg.Token, arms, t.ID, mapper, selSeed, r.cfg.Timeout)
		if err != nil {
			r.Shutdown()
			return err
		}
		r.gateways[t.ID] = g
	}
	return nil
}

// Shutdown kills all gateways and closes the progress log.
func (r *Runner) Shutdown() {
	for _, g := range r.gateways {
		g.Kill()
	}
	if r.progress != nil {
		_ = r.progress.Sync()
		_ = r.progress.Close()
		r.progress = nil
	}
}

func (r *Runner) logProgress(row ProgressRow) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return r.logProgressRaw(append(b, '\n'))
}

func (r *Runner) logProgressRaw(b []byte) error {
	if _, err := r.progress.Write(b); err != nil {
		return err
	}
	return r.progress.Sync()
}

// RunHeader is the first progress-log line of a run. Resume binds to every
// field: a different experiment, workload, seed, charter, strategy set, or
// clock is a different experiment and must not continue this ledger.
type RunHeader struct {
	Kind            string `json:"kind"`
	ExperimentID    string `json:"experiment_id"`
	WorkloadVersion string `json:"workload_version"`
	Seed            uint64 `json:"seed"`
	CharterDigest   string `json:"charter_digest,omitempty"`
	TreatmentsHash  string `json:"treatments_hash"`
	T0Clock         string `json:"t0_clock"`
	StepSeconds     int64  `json:"step_seconds"`
}

// treatmentConfigHash binds the strategy/treatment configuration.
func treatmentConfigHash(m *Manifest) (string, error) {
	b, err := json.Marshal(m.Treatments)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:16]), nil
}

// currentHeader builds the header this run requires.
func (r *Runner) currentHeader() (RunHeader, error) {
	th, err := treatmentConfigHash(r.cfg.Manifest)
	if err != nil {
		return RunHeader{}, err
	}
	return RunHeader{
		Kind: "run-header", ExperimentID: r.cfg.Manifest.ExperimentID,
		WorkloadVersion: r.cfg.Manifest.WorkloadVersion, Seed: r.cfg.Manifest.Seed,
		CharterDigest: r.cfg.Manifest.CharterDigest, TreatmentsHash: th,
		T0Clock:     r.cfg.T0Clock.UTC().Format(time.RFC3339Nano),
		StepSeconds: int64(r.cfg.Step / time.Second),
	}, nil
}

// checkOrWriteHeader writes the header on a fresh root, or rejects resume
// under incompatible configuration.
func (r *Runner) checkOrWriteHeader() error {
	want, err := r.currentHeader()
	if err != nil {
		return err
	}
	f, err := os.Open(r.cfg.Root + "/progress.jsonl")
	if os.IsNotExist(err) {
		return r.logProgressRaw(headerLine(want))
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	if !sc.Scan() {
		// Empty file: treat as fresh.
		return r.logProgressRaw(headerLine(want))
	}
	var got RunHeader
	if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
		return fmt.Errorf("exp-run: progress log has no run header (foreign file?): %w", err)
	}
	if got.Kind != "run-header" {
		return fmt.Errorf("exp-run: progress log has no run header (foreign file?)")
	}
	mismatch := func(field, a, b string) error {
		if a != b {
			return fmt.Errorf("exp-run: resume incompatible: %s changed (%q vs %q)", field, b, a)
		}
		return nil
	}
	if err := mismatch("experiment_id", want.ExperimentID, got.ExperimentID); err != nil {
		return err
	}
	if err := mismatch("workload_version", want.WorkloadVersion, got.WorkloadVersion); err != nil {
		return err
	}
	if want.Seed != got.Seed {
		return fmt.Errorf("exp-run: resume incompatible: seed changed (%d vs %d)", got.Seed, want.Seed)
	}
	if want.CharterDigest != got.CharterDigest {
		return fmt.Errorf("exp-run: resume incompatible: charter digest changed (%q vs %q)", got.CharterDigest, want.CharterDigest)
	}
	if err := mismatch("treatments", want.TreatmentsHash, got.TreatmentsHash); err != nil {
		return err
	}
	if err := mismatch("t0_clock", want.T0Clock, got.T0Clock); err != nil {
		return err
	}
	if want.StepSeconds != got.StepSeconds {
		return fmt.Errorf("exp-run: resume incompatible: step changed (%d vs %d)", got.StepSeconds, want.StepSeconds)
	}
	return nil
}

func headerLine(h RunHeader) []byte {
	b, err := json.Marshal(h)
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

// LoadProgress reads the latest row per job, skipping the run header.
func LoadProgress(root string) (map[string]ProgressRow, error) {
	out := map[string]ProgressRow{}
	f, err := os.Open(root + "/progress.jsonl")
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var row ProgressRow
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("exp-run: bad progress line: %w", err)
		}
		if row.JobID == "" {
			continue // run header or foreign line: headers are checked separately
		}
		out[row.JobID] = row
	}
	return out, sc.Err()
}

// Run executes all jobs: assignment for every eligible job first is
// guaranteed by computing (and persisting) each job's assignment before its
// first execution; resume skips terminally-settled jobs and re-runs the rest
// from scratch with identical assignments.
func (r *Runner) Run(ctx context.Context) error {
	// Resume binds to the run header: experiment, workload version, seed,
	// charter, treatments, and clock must all match, or resume refuses.
	if err := r.checkOrWriteHeader(); err != nil {
		return err
	}
	prior, err := LoadProgress(r.cfg.Root)
	if err != nil {
		return err
	}
	for idx := range r.cfg.Manifest.Jobs {
		job := &r.cfg.Manifest.Jobs[idx]
		assignedAt := SimClock(r.cfg.T0Clock, r.cfg.Step, idx)
		all := treatmentIDs(r.cfg.Manifest)
		asg, err := r.assigner.Assign(job.JobID, job.Strata)
		if err != nil {
			return err
		}
		// Restrict to eligible treatments only if the manifest says so.
		if elig := r.cfg.Manifest.EligibleTreatments(*job, all); !contains(elig, asg.Treatment) {
			asg, err = r.assignEligible(job, elig)
			if err != nil {
				return err
			}
		}
		if prev, ok := prior[job.JobID]; ok {
			if prev.Treatment != asg.Treatment {
				return fmt.Errorf("exp-run: job %q treatment changed across restart (%q vs %q): refusing",
					job.JobID, prev.Treatment, asg.Treatment)
			}
			if prev.Terminal {
				continue
			}
		}
		if err := r.runJob(ctx, *job, asg, assignedAt, idx); err != nil {
			return err
		}
		r.doneJobs++
		if r.cfg.CrashAfter > 0 && r.doneJobs >= r.cfg.CrashAfter {
			// Deterministic crash injection (resume-proof testing only):
			// SIGKILL the gateways with no graceful checkpoint, then die
			// without another write. Progress rows are fsync'd per write,
			// so resume sees exactly the completed prefix. Never used in
			// production paths.
			for _, g := range r.gateways {
				if g.cmd != nil && g.cmd.Process != nil {
					_ = g.cmd.Process.Signal(syscall.SIGKILL)
					_, _ = g.cmd.Process.Wait()
				}
			}
			_ = r.progress.Sync()
			os.Exit(3)
		}
	}
	// Late phase: corrections, delayed terminals, in manifest order.
	for _, l := range r.late {
		l.ev.StrategyID = l.tx
		g := r.gateways[l.tx]
		if _, _, err := g.Settle(l.ev); err != nil {
			return fmt.Errorf("exp-run: late settle %s: %w", l.ev.JobID, err)
		}
		if err := r.logProgress(ProgressRow{JobID: l.manifestJob, Treatment: l.tx,
			Phase: PhaseComplete, Versions: []uint64{l.ev.Version}, Terminal: true}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) assignEligible(job *ManifestJob, elig []string) (harness.Assignment, error) {
	// Deterministic restriction: re-hash within the eligible subset by
	// suffixing the strata (same domain, stable across restarts).
	a := harness.Assigner{Seed: r.assigner.Seed, Treatments: elig}
	w := make([]float64, len(elig))
	for i := range w {
		w[i] = 1
	}
	a.Weights = w
	return a.Assign(job.JobID, job.Strata+"\x00eligible")
}

func treatmentIDs(m *Manifest) []string {
	out := make([]string, len(m.Treatments))
	for i, t := range m.Treatments {
		out[i] = t.ID
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// runJob executes one job's attempt loop through its treatment gateway,
// verifies, and settles v1. Multi-version series queue their later versions
// for the late phase.
func (r *Runner) runJob(ctx context.Context, job ManifestJob, asg harness.Assignment, assignedAt time.Time, idx int) error {
	tx := r.txConfig(asg.Treatment)
	g := r.gateways[asg.Treatment]
	// Assignment lands in the treatment ledger BEFORE any execution, so the
	// offline report can join assignments to outcomes per treatment.
	asg.AssignedAt = assignedAt.UTC().Format(time.RFC3339Nano)
	if !r.assigned[asg.Treatment+"\x00"+asg.JobID] {
		if err := appendAssignment(r.cfg.Root+"/"+asg.Treatment, asg); err != nil {
			return err
		}
		r.assigned[asg.Treatment+"\x00"+asg.JobID] = true
	}
	if err := r.logProgress(ProgressRow{JobID: job.JobID, Treatment: asg.Treatment,
		Probability: asg.Probability, Phase: PhaseAssigned}); err != nil {
		return err
	}
	var attempts []outcome.Attempt
	var decisions []string
	var firstDecision, firstJob string
	for att := 0; att < tx.MaxAttempts; att++ {
		rr := r.executeAttempt(ctx, g, job, att, assignedAt)
		if rr.TimedOut {
			arm, dec := r.resolveTimeout(g, assignedAt)
			if dec == "" {
				// No decision was committed for this attempt (the request
				// never reached the gateway, or the ledger is unreadable).
				// Abort the job with a distinct error: fabricating a
				// placeholder gateway ID (e.g. "job-") would collide across
				// jobs and corrupt the join. The job stays visibly
				// attempted-but-unsettled for operator triage.
				return fmt.Errorf("exp-run: job %q attempt %d timed out with no committed decision: aborting (no placeholder created)", job.JobID, att)
			}
			obs := ObservedAttempt{DecisionID: dec, Transport: outcome.TransportTimeout, TimeoutMs: float64(r.cfg.Timeout.Milliseconds())}
			plan := r.verifier.PlanAttempt(job, r.cfg.Manifest.ExperimentID, att, arm, obs, assignedAt)
			attempts = append(attempts, plan.Attempt)
			decisions = append(decisions, dec)
			if firstDecision == "" {
				firstDecision, firstJob = dec, "job-"+dec
			}
			continue
		}
		if rr.DecisionID == "" {
			return fmt.Errorf("exp-run: job %q attempt %d: gateway returned no decision", job.JobID, att)
		}
		obs := ObservedAttempt{DecisionID: rr.DecisionID, Arm: rr.SelectedArm,
			Transport: transportOf(rr.HTTPStatus), HTTPStatus: rr.HTTPStatus}
		plan := r.verifier.PlanAttempt(job, r.cfg.Manifest.ExperimentID, att, rr.SelectedArm, obs, assignedAt)
		attempts = append(attempts, plan.Attempt)
		decisions = append(decisions, rr.DecisionID)
		if firstDecision == "" {
			firstDecision, firstJob = rr.DecisionID, rr.JobID
		}
		if firstJob == "" {
			firstJob = "job-" + rr.DecisionID
		}
		if plan.TerminalSuccess {
			break
		}
	}
	if err := r.logProgress(ProgressRow{JobID: job.JobID, Treatment: asg.Treatment,
		Probability: asg.Probability, Phase: PhaseAttempted, Decisions: decisions}); err != nil {
		return err
	}
	// Join row before settlement: crash between here and v1 leaves a mapped
	// but unsettled job, which resume re-runs (the superseding row wins).
	if firstJob == "" {
		firstJob = "job-" + firstDecision
	}
	if err := r.recordJobMap(asg.Treatment, job.JobID, firstJob); err != nil {
		return err
	}
	versions, settle := r.verifier.PlanSettlement(job, r.cfg.Manifest.ExperimentID, firstDecision, firstJob, attempts, assignedAt)
	if !settle {
		// Unresolved: visibly unsettled, never fabricated.
		return r.logProgress(ProgressRow{JobID: job.JobID, Treatment: asg.Treatment,
			Probability: asg.Probability, Phase: PhaseAttempted, Decisions: decisions})
	}
	for i, ev := range versions {
		ev.StrategyID = asg.Treatment
		if i == 0 {
			if _, _, err := g.Settle(ev); err != nil {
				return fmt.Errorf("exp-run: settle %s v%d: %w", job.JobID, ev.Version, err)
			}
			if err := r.logProgress(ProgressRow{JobID: job.JobID, Treatment: asg.Treatment,
				Probability: asg.Probability, Phase: PhaseSettledV1, Decisions: decisions,
				Versions: []uint64{ev.Version}, Terminal: len(versions) == 1}); err != nil {
				return err
			}
		} else {
			r.late = append(r.late, lateSettlement{tx: asg.Treatment, manifestJob: job.JobID, ev: ev})
		}
	}
	_ = idx
	return nil
}

// executeAttempt POSTs one attempt. Timeout behavior uses a short deadline
// with a server-side delay so the timeout is real and ambiguous.
func (r *Runner) executeAttempt(ctx context.Context, g *GatewayProc, job ManifestJob, att int, assignedAt time.Time) RouteResult {
	_ = assignedAt
	deadline := 30 * time.Second
	body := []byte(`{"job":"` + job.JobID + `"}`)
	if job.Behavior == BehaviorTimeoutThenAccept && att == 0 {
		deadline = 500 * time.Millisecond
		return g.RouteWithHeaders(ctx, body, deadline, map[string]string{"X-Fake-Delay-Ms": "5000"})
	}
	return g.Route(ctx, body, deadline)
}

// resolveTimeout finds the timed-out attempt's decision in the treatment
// ledger. The server was still sleeping when the client deadline fired, so
// the commit lands after we start looking: poll until a new committed line
// appears. The runner is strictly sequential, so that decision belongs to
// the in-flight attempt. Returns the selected arm and decision ID.
func (r *Runner) resolveTimeout(g *GatewayProc, since time.Time) (arm, decision string) {
	_ = since
	before := countLines(g.Dir + "/decisions.jsonl")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countLines(g.Dir+"/decisions.jsonl") > before {
			return latestDecision(g.Dir)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return latestDecision(g.Dir)
}

func countLines(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// appendAssignment persists one assignment row to a treatment ledger,
// creating the row file on first use. fsync-per-row: assignment must survive
// a crash before execution begins.
func appendAssignment(dir string, asg harness.Assignment) error {
	f, err := os.OpenFile(dir+"/assignments.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(asg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

func (r *Runner) txConfig(id string) TreatmentConfig {
	for _, t := range r.cfg.Manifest.Treatments {
		if t.ID == id {
			return t
		}
	}
	return TreatmentConfig{MaxAttempts: 1}
}

func transportOf(status int) outcome.TransportStatus {
	if status >= 200 && status < 300 {
		return outcome.TransportOK
	}
	return outcome.TransportError
}

// latestDecision scans the treatment decision ledger for the newest
// committed decision. Returns selected arm, then decision ID.
func latestDecision(dir string) (arm, decision string) {
	f, err := os.Open(dir + "/decisions.jsonl")
	if err != nil {
		return "", ""
	}
	defer f.Close()
	type rec struct {
		Type      string `json:"type"`
		Committed *struct {
			DecisionID    string `json:"decision_id"`
			SelectedArmID string `json:"selected_arm_id"`
		} `json:"committed,omitempty"`
	}
	var lastID, lastArm string
	sc := bufioScanner(f)
	for sc.Scan() {
		var r rec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if r.Type == "committed" && r.Committed != nil {
			lastID, lastArm = r.Committed.DecisionID, r.Committed.SelectedArmID
		}
	}
	return lastArm, lastID
}

func bufioScanner(f *os.File) *bufio.Scanner {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	return sc
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
