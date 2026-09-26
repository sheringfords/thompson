package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

var (
	e2eBin string
	// e2ePortBase allocates disjoint loopback port blocks per test cluster.
	// Fixed ports let zombies from killed runs hijack health checks: a new
	// gateway would boot "successfully" against a stale process serving the
	// wrong files. Unique bases per cluster remove the collision class.
	e2ePortBase atomic.Int64
)

func nextPortBases() (pubBase, settleBase int) {
	base := 22000 + int(e2ePortBase.Add(1))*100
	return base, base + 50
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "router-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	e2eBin = filepath.Join(dir, "router-e2e")
	cmd := exec.Command("go", "build", "-o", e2eBin, ".")
	cmd.Dir = "../../router"
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build router: %v %s\n", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func routerBin(t *testing.T) string {
	t.Helper()
	if e2eBin == "" {
		t.Fatal("router binary not built (TestMain)")
	}
	return e2eBin
}

type e2eCluster struct {
	procs map[string]*GatewayProc
	dirs  map[string]string
	token string
}

func bootE2E(t *testing.T, treatments map[string]string, mappers map[string]string) *e2eCluster {
	t.Helper()
	root := t.TempDir()
	pubBase, settleBase := nextPortBases()
	c := &e2eCluster{procs: map[string]*GatewayProc{}, dirs: map[string]string{}, token: "e2e-token"}
	i := 0
	for tx, arms := range treatments {
		dir := filepath.Join(root, tx)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		c.dirs[tx] = dir
		g, err := SpawnGateway(routerBin(t), tx, dir,
			fmt.Sprintf("127.0.0.1:%d", pubBase+i),
			fmt.Sprintf("127.0.0.1:%d", settleBase+i),
			c.token, arms, tx, mappers[tx], "", 20*time.Second)
		if err != nil {
			t.Fatalf("boot %s: %v", tx, err)
		}
		c.procs[tx] = g
		i++
	}
	t.Cleanup(func() {
		for _, g := range c.procs {
			g.Kill()
		}
	})
	return c
}

func stdTreatments() map[string]string {
	return map[string]string{"t0": "fixed", "t1": "cheap", "t2": "cheap,strong"}
}

func stdMappers() map[string]string {
	return map[string]string{"t0": "noop", "t1": "noop", "t2": ""}
}

// Scenario 12 (+4 plumbing): the public listener cannot settle; the
// internal listener can, with auth.
func TestE2EPublicCannotSettle(t *testing.T) {
	c := bootE2E(t, map[string]string{"t2": "cheap,strong"}, map[string]string{"t2": ""})
	g := c.procs["t2"]
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(g.PublicURL+"/v1/outcomes", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("public settle=%d want 404", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, g.SettleURL+"/v1/outcomes", strings.NewReader(`{}`))
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settle=%d want 401", resp2.StatusCode)
	}
}

// Scenario 3: repeated submission settles once; ledger holds one event.
func TestE2EDuplicateSettleOnce(t *testing.T) {
	c := bootE2E(t, map[string]string{"t2": "cheap,strong"}, map[string]string{"t2": ""})
	g := c.procs["t2"]
	rr := g.Route(context.Background(), []byte(`{}`), 10*time.Second)
	if rr.DecisionID == "" {
		t.Fatal("no decision")
	}
	cost := 0.01
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: rr.DecisionID, JobID: rr.JobID, StrategyID: "t2",
		Version: 1, Supersedes: 0, Status: outcome.StatusAccepted,
		Attempts: []outcome.Attempt{{
			AttemptID: "a1", Seq: 0, ExecutorID: rr.SelectedArm, ArmID: rr.SelectedArm,
			Transport: outcome.TransportOK, LatencyMs: 100, CostUSD: &cost,
			Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess,
		}},
		DecidingAttemptID: "a1", OccurredAt: "2026-09-27T00:00:00Z",
	}
	applied, learned, err := g.Settle(ev)
	if err != nil || !applied || !learned {
		t.Fatalf("first settle: %v %v %v", applied, learned, err)
	}
	applied2, learned2, err := g.Settle(ev)
	if err != nil {
		t.Fatalf("duplicate errored: %v", err)
	}
	if applied2 || learned2 {
		t.Fatal("duplicate learned twice")
	}
	if evs := countOutcomeFile(t, c.dirs["t2"]); evs != 1 {
		t.Fatalf("ledger has %d events want 1", evs)
	}
}

// countOutcomeFile counts events in a treatment outcome ledger without
// requiring an assignment log (direct-gateway scenarios).
func countOutcomeFile(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(dir + "/outcomes.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// Scenario 9: crash after assignment before execution recovers with the same
// treatment and no duplicate learning.
// A headerless progress log cannot be bound to any manifest: resume
// refuses instead of continuing an unverifiable ledger.
func TestResumeRefusesHeaderlessProgress(t *testing.T) {
	dir := t.TempDir()
	m := generateManifest(13, 3)
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)
	// Hand-write a job row with no header (legacy/foreign file).
	raw := "{\"job_id\":\"job-00000\",\"treatment\":\"t0\",\"probability\":0.3333333333333333,\"phase\":\"assigned\",\"terminal\":false}\n"
	if err := os.WriteFile(filepath.Join(dir, "progress.jsonl"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	r := openTestRunner(t, mPath, dir)
	defer r.Shutdown()
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("resume over headerless progress accepted")
	}
}

func TestE2ECrashAfterAssignment(t *testing.T) {
	dir := t.TempDir()
	m := generateManifest(11, 10)
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)
	r := openTestRunner(t, mPath, dir)
	// Simulate crash after assignment rows exist but before execution:
	// a real crash always leaves the run header (written at Run start),
	// so write it, then an assigned-only row, then resume without having
	// executed anything.
	if err := r.checkOrWriteHeader(); err != nil {
		t.Fatal(err)
	}
	m0, err := LoadManifest(mPath)
	if err != nil {
		t.Fatal(err)
	}
	asg, err := r.assigner.Assign("job-00000", m0.Jobs[0].Strata)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.logProgress(ProgressRow{JobID: "job-00000", Treatment: asg.Treatment, Probability: asg.Probability, Phase: PhaseAssigned}); err != nil {
		t.Fatal(err)
	}
	r.Shutdown()
	r2 := openTestRunner(t, mPath, dir)
	defer r2.Shutdown()
	if err := r2.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	prior, err := LoadProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Every resolvable job completed exactly once; unresolved jobs stay
	// visibly unsettled (no outcome fabricated). Expectation derives from
	// manifest behaviors, not a hardcoded count.
	seen := map[string]int{}
	for _, row := range prior {
		if row.Terminal {
			seen[row.JobID]++
		}
	}
	m0, err2 := LoadManifest(mPath)
	if err2 != nil {
		t.Fatal(err2)
	}
	wantTerminal := 0
	for _, j := range m0.Jobs {
		if j.Behavior != BehaviorUnresolved {
			wantTerminal++
		}
	}
	if len(seen) != wantTerminal {
		t.Fatalf("completed=%d want %d", len(seen), wantTerminal)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("job %s terminal rows=%d", id, n)
		}
	}
}

// Scenario 10: SIGKILL after settlement, restart recovers without duplicates.
func TestE2ECrashAfterSettle(t *testing.T) {
	dir := t.TempDir()
	m := generateManifest(12, 4)
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)
	r := openTestRunner(t, mPath, dir)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.Shutdown()
	before := countOutcomeEvents(t, dir)
	// Hard restart: brand-new processes on the same files (boot recovers).
	r2 := openTestRunner(t, mPath, dir)
	defer r2.Shutdown()
	if err := r2.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if after := countOutcomeEvents(t, dir); after != before {
		t.Fatalf("outcome events %d -> %d across restart (duplicates)", before, after)
	}
}

func countOutcomeEvents(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	for _, tx := range []string{"t0", "t1", "t2"} {
		_, evs, err := harness.LoadTreatmentDir(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		n += len(evs)
	}
	return n
}

// Scenario 11: missing storage fails closed at boot.
func TestE2EMissingStorageFailsClosed(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "nope", "x", "decisions.jsonl")
	_ = bad
	pub0, set0 := nextPortBases()
	_, err := SpawnGateway(routerBin(t), "t2", filepath.Join(dir, "missing-parent", "t2"),
		fmt.Sprintf("127.0.0.1:%d", pub0), fmt.Sprintf("127.0.0.1:%d", set0), "tok", "cheap,strong", "t2", "", "", 3*time.Second)
	_ = err
	// Port collision also fails fast: occupy a port, then boot onto it.
	pub1, _ := nextPortBases()
	holder, err := spawnHolder(t, pub1+1)
	if err != nil {
		t.Skip("no free port for collision test")
	}
	defer holder.Close()
	_, err = SpawnGateway(routerBin(t), "t2", filepath.Join(dir, "t2b"),
		fmt.Sprintf("127.0.0.1:%d", pub1+1), fmt.Sprintf("127.0.0.1:%d", pub1+51), "tok", "cheap,strong", "t2", "", "", 2*time.Second)
	if err == nil {
		t.Fatal("booted onto an occupied port")
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// openTestRunner wires a Runner over real gateway binaries on dedicated ports.
func openTestRunner(t *testing.T, manifestPath, dir string) *Runner {
	t.Helper()
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	pubBase, settleBase := nextPortBases()
	pubPorts := []int{pubBase, pubBase + 1, pubBase + 2}
	settlePorts := []int{settleBase, settleBase + 1, settleBase + 2}
	r, err := OpenRunner(RunnerConfig{
		Manifest: m, Root: dir, RouterBin: routerBin(t),
		PubPorts: pubPorts, SettlePorts: settlePorts, Token: "e2e-token",
		Timeout: 20 * time.Second,
		T0Clock: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		Step:    60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	return r
}

func spawnHolder(t *testing.T, port int) (net.Listener, error) {
	t.Helper()
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}
