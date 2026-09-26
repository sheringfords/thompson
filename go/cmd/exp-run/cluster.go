package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

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
	cmd       *exec.Cmd
	client    *http.Client
}

// SpawnGateway starts a router binary with per-treatment files in verified
// mode. Each treatment gets its own process, policy, learner, and ledgers:
// no cross-treatment state exists by construction.
func SpawnGateway(routerBin, treatment, dir, publicAddr, settleAddr, token, arms, strategy, mapper, selectionSeed string, timeout time.Duration) (*GatewayProc, error) {
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
	cmd := exec.Command(routerBin)
	cmd.Env = env
	// Mutex-guarded stderr: os/exec copies child output from its own
	// goroutine while the health-wait loop below may read the buffer on
	// failure. A plain bytes.Buffer races here (caught by -race).
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exp-run: start gateway %s: %w", treatment, err)
	}
	g := &GatewayProc{
		Treatment: treatment,
		PublicURL: "http://" + publicAddr,
		SettleURL: "http://" + settleAddr,
		Token:     token, Dir: dir, cmd: cmd,
		client: &http.Client{Timeout: timeout},
	}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := g.client.Get(g.PublicURL + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return g, nil
		}
		if err == nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			return nil, fmt.Errorf("exp-run: gateway %s unhealthy: %s", treatment, stderr.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}

// lockedBuffer is a bytes.Buffer safe for concurrent use by os/exec's
// stderr copier and the spawning goroutine's failure reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Kill stops the gateway and waits for exit.
func (g *GatewayProc) Kill() {
	if g.cmd != nil && g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
		_, _ = g.cmd.Process.Wait()
	}
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
