package main

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/journal"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// GatewayProc is one treatment's router binary: public inference listener +
// internal settlement listener, loopback-only in tests and dry runs.
type GatewayProc struct {
	Treatment string
	PublicURL string
	SettleURL string
	Token     string
	Dir       string
	// instanceID binds this client to its exact child process: every request
	// carries it as X-Expect-Instance and boot refuses a mismatched
	// X-Instance-ID, so a squatter on our ports fails loudly (409 / boot
	// error) instead of silently serving foreign ledgers.
	instanceID string
	cmd        *exec.Cmd
	client     *http.Client
	// countDecisions/latestCommitted abstract timeout resolution over the
	// treatment's decision authority (file ledger or journal). Set at spawn;
	// resolveTimeout never touches backend files directly.
	countDecisions  func() int
	latestCommitted func() (arm, id string)
}

// newInstanceID mints a unique per-gateway-process identity.
func newInstanceID() string {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return fmt.Sprintf("e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b[:])
}

// syncBuffer is a goroutine-safe bytes.Buffer for capturing child stderr.
// os/exec writes to Cmd.Stderr from background goroutines while
// SpawnGateway's health-poll loop reads the capture on the failure path;
// bytes.Buffer alone races under -race.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// SpawnGateway starts a router binary with per-treatment files in verified
// mode. Each treatment gets its own process, policy, learner, and ledgers:
// no cross-treatment state exists by construction. Identity binding (ports +
// instance handshake) lives in spawnWithEnv, shared by all spawn paths.
func SpawnGateway(routerBin, treatment, dir, publicAddr, settleAddr, token, arms, strategy, mapper, selectionSeed string, timeout time.Duration) (*GatewayProc, error) {
	return spawnWithEnv(routerBin, treatment, dir, publicAddr, settleAddr, token, arms, strategy, mapper, selectionSeed, map[string]string{}, timeout)
}

func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}

// kill stops the gateway and waits for exit.
func (g *GatewayProc) kill() {
	if g.cmd != nil && g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
		_, _ = g.cmd.Process.Wait()
	}
}

// Kill stops the gateway and waits for exit (public alias for tests).
func (g *GatewayProc) Kill() {
	g.kill()
}

// RouteResult is one attempt's transport observation.
type RouteResult struct {
	DecisionID  string
	JobID       string
	SelectedArm string
	HTTPStatus  int
	TimedOut    bool
}

// Route POSTs one inference request with a client deadline. A fired deadline
// is genuinely ambiguous (the server may have executed) and reported as a
// timeout, never as success or failure.
func (g *GatewayProc) Route(ctx context.Context, body []byte, deadline time.Duration) RouteResult {
	return g.RouteWithHeaders(ctx, body, deadline, nil)
}

// RouteWithHeaders adds request headers (e.g. X-Fake-Delay-Ms for
// deterministic server-side slowness through FakeProviders).
func (g *GatewayProc) RouteWithHeaders(ctx context.Context, body []byte, deadline time.Duration, headers map[string]string) RouteResult {
	cctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, g.PublicURL+"/", bytes.NewReader(body))
	if err != nil {
		return RouteResult{TimedOut: true}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if g.instanceID != "" {
		req.Header.Set("X-Expect-Instance", g.instanceID)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return RouteResult{TimedOut: true}
	}
	defer resp.Body.Close()
	return RouteResult{
		DecisionID:  resp.Header.Get("X-Decision-ID"),
		JobID:       resp.Header.Get("X-Job-ID"),
		SelectedArm: resp.Header.Get("X-Selected-Arm"),
		HTTPStatus:  resp.StatusCode,
	}
}

// Settle posts one versioned outcome to the internal listener. Only 2xx
// responses with applied=true mean durable; anything else is an error.
func (g *GatewayProc) Settle(ev outcome.OutcomeEvent) (applied, learned bool, err error) {
	ev.StrategyID = g.Treatment
	b, err := json.Marshal(ev)
	if err != nil {
		return false, false, err
	}
	req, err := http.NewRequest(http.MethodPost, g.SettleURL+"/v1/outcomes", bytes.NewReader(b))
	if err != nil {
		return false, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	if g.instanceID != "" {
		req.Header.Set("X-Expect-Instance", g.instanceID)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return false, false, fmt.Errorf("exp-run: settle %s v%d: %w", ev.JobID, ev.Version, err)
	}
	defer resp.Body.Close()
	var out struct {
		Applied bool   `json:"applied"`
		Learned bool   `json:"learned"`
		Status  string `json:"status"`
		Version uint64 `json:"outcome_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, false, fmt.Errorf("exp-run: settle %s v%d status=%d decode: %w", ev.JobID, ev.Version, resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, false, fmt.Errorf("exp-run: settle %s v%d rejected: %d", ev.JobID, ev.Version, resp.StatusCode)
	}
	return out.Applied, out.Learned, nil
}

// SpawnCostAwareGateway starts a router binary in supervised cost-aware mode:
// verified learning plus the validated RuleV2/V3 policy, durable safety
// controller, and operator authentication. All three inputs are required;
// absence fails closed in the binary (never half-enabled).
func SpawnCostAwareGateway(routerBin, treatment, dir, publicAddr, settleAddr, token, arms, strategy, selectionSeed, safetyConfigPath, safetyPath, operatorToken, journalPath string, timeout time.Duration) (*GatewayProc, error) {
	env := map[string]string{
		"COSTAWARE":      "1",
		"SAFETY_CONFIG":  safetyConfigPath,
		"SAFETY_PATH":    safetyPath,
		"OPERATOR_TOKEN": operatorToken,
	}
	if journalPath != "" {
		if len(journalPath) > 16 && journalPath[:16] == "JOURNAL-BACKEND-" {
			return nil, fmt.Errorf("exp-run: treatment %s has %s (refusing)", treatment, journalPath)
		}
		env["JOURNAL_PATH"] = journalPath
	}
	base, err := spawnWithEnv(routerBin, treatment, dir, publicAddr, settleAddr, token, arms, strategy, "", selectionSeed, env, timeout)
	if err != nil {
		return nil, err
	}
	if journalPath != "" {
		// Timeout resolution reads the journal authority, never the
		// (absent) decisions.jsonl. Read handles are short-lived per
		// call: no lifecycle coupling to the gateway process.
		jp := journalPath
		base.countDecisions = func() int { return countJournalDecisions(jp) }
		base.latestCommitted = func() (string, string) { return latestJournalDecision(jp) }
	}
	return base, err
}

func spawnWithEnv(routerBin, treatment, dir, publicAddr, settleAddr, token, arms, strategy, mapper, selectionSeed string, extra map[string]string, timeout time.Duration) (*GatewayProc, error) {
	instanceID := newInstanceID()
	extra["INSTANCE_ID"] = instanceID
	env := append(os.Environ(),
		"ROUTER_MODE=verified",
		"STRATEGY_ID="+strategy,
		"ARMS="+arms,
		"PORT="+portOf(publicAddr),
		"SETTLE_ADDR="+settleAddr,
		"EVIDENCE_PATH="+dir+"/evidence.jsonl",
		"DECISIONS_PATH="+dir+"/decisions.jsonl",
		"OUTCOMES_PATH="+dir+"/outcomes.jsonl",
		"SETTLE_TOKEN="+token,
		"MAPPER="+mapper,
		"SELECTION_SEED="+selectionSeed,
	)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	cmd := exec.Command(routerBin)
	cmd.Env = env
	var stderr syncBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exp-run: start gateway %s: %w", treatment, err)
	}
	g := &GatewayProc{
		Treatment: treatment,
		PublicURL: "http://" + publicAddr,
		SettleURL: "http://" + settleAddr,
		Token:     token, Dir: dir, cmd: cmd, instanceID: instanceID,
		client:          &http.Client{Timeout: timeout},
		countDecisions:  func() int { n, _ := countFileDecisions(dir); return n },
		latestCommitted: func() (string, string) { return latestDecision(dir) },
	}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := g.client.Get(g.PublicURL + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			got := resp.Header.Get("X-Instance-ID")
			resp.Body.Close()
			if got != instanceID {
				g.kill()
				if got == "" {
					return nil, fmt.Errorf("exp-run: gateway %s health check has no instance identity (foreign or legacy process on our ports?)", treatment)
				}
				return nil, fmt.Errorf("exp-run: gateway %s is a different process (refusing cross-talk)", treatment)
			}
			return g, nil
		}
		if err == nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, waitErr := cmd.Process.Wait()
			// Report the child exit status: a fast silent death (bad
			// config, missing files, signal) looks identical to a hang
			// from the health poll alone.
			if waitErr != nil {
				return nil, fmt.Errorf("exp-run: gateway %s unhealthy (child exit: %v): %s", treatment, waitErr, stderr.String())
			}
			return nil, fmt.Errorf("exp-run: gateway %s unhealthy: %s", treatment, stderr.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// countFileDecisions counts committed decision rows in a file ledger.
func countFileDecisions(dir string) (int, error) {
	raw, err := os.ReadFile(dir + "/decisions.jsonl")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}

// countJournalDecisions counts committed decision rows through a
// short-lived read handle (WAL readers never contend with the writer).
func countJournalDecisions(journalPath string) int {
	j, err := journal.Open(journalPath)
	if err != nil {
		return 0
	}
	defer j.Close()
	evs, err := j.EventsSince(0)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "decision" {
			n++
		}
	}
	return n
}

// latestJournalDecision returns the newest committed decision.
func latestJournalDecision(journalPath string) (arm, id string) {
	j, err := journal.Open(journalPath)
	if err != nil {
		return "", ""
	}
	defer j.Close()
	evs, err := j.EventsSince(0)
	if err != nil {
		return "", ""
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != "decision" {
			continue
		}
		var d journal.Decision
		if err := jsonUnmarshalToDecision(evs[i].Payload, &d); err != nil {
			continue
		}
		return d.SelectedArm, d.DecisionID
	}
	return "", ""
}

// Operator posts an operator action (suspend/resume) to the treatment's
// internal listener with the operator credential.
func (g *GatewayProc) Operator(action, arm, reason, operatorToken, operatorID string) (int, error) {
	body, _ := json.Marshal(map[string]string{"arm": arm, "reason": reason})
	req, err := http.NewRequest(http.MethodPost, g.SettleURL+"/v1/operator/"+action, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+operatorToken+":"+operatorID)
	if g.instanceID != "" {
		req.Header.Set("X-Expect-Instance", g.instanceID)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func jsonUnmarshalToDecision(raw string, d *journal.Decision) error {
	return json.Unmarshal([]byte(raw), d)
}
