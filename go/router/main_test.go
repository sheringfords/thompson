package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

func envGetter(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoadConfigLegacyDefaults(t *testing.T) {
	cfg, err := loadConfig(envGetter(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.port != "8080" || cfg.evidencePath != "./evidence.jsonl" || len(cfg.arms) != 2 {
		t.Fatalf("bad defaults: %+v", cfg)
	}
	if cfg.mode != "legacy" {
		t.Fatalf("mode=%q want legacy", cfg.mode)
	}
}

func TestLoadConfigVerifiedRequiresSecrets(t *testing.T) {
	base := map[string]string{"ROUTER_MODE": "verified", "STRATEGY_ID": "t2"}
	if _, err := loadConfig(envGetter(base)); err == nil {
		t.Fatal("verified without paths/token loaded (must fail closed)")
	}
	full := map[string]string{
		"ROUTER_MODE": "verified", "STRATEGY_ID": "t2",
		"DECISIONS_PATH": "/tmp/d.jsonl", "OUTCOMES_PATH": "/tmp/o.jsonl",
		"SETTLE_TOKEN": "s3cret",
	}
	cfg, err := loadConfig(envGetter(full))
	if err != nil {
		t.Fatalf("full verified config rejected: %v", err)
	}
	if cfg.settleAddr != "127.0.0.1:8081" {
		t.Fatalf("settleAddr=%q", cfg.settleAddr)
	}
	if _, err := loadConfig(envGetter(map[string]string{"ROUTER_MODE": "nope"})); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := loadConfig(envGetter(map[string]string{"SHADOW_SAMPLE_RATE": "x"})); err == nil {
		t.Fatal("bad shadow rate accepted")
	}
}

func TestBuildRouterVerifiedOpensDurableStores(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(envGetter(map[string]string{
		"ROUTER_MODE": "verified", "STRATEGY_ID": "t2",
		"ARMS":           "a,b",
		"EVIDENCE_PATH":  filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH": filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH":  filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN":   "s3cret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	rt, cleanup, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("verified build: %v", err)
	}
	defer cleanup()
	// Second builder on the same files fails: single-writer enforced.
	if _, _, err := buildRouter(cfg); err == nil {
		t.Fatal("second verified builder opened locked files")
	}

	// Settlement reachable ONLY on the internal mux, auth-gated.
	pub, priv := publicMux(rt), internalMux(rt)

	rec := httptest.NewRecorder()
	pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/outcomes", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public settle=%d want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	priv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/outcomes", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settle=%d want 401", rec.Code)
	}
}

// TestProductionRouteSettlement exercises the real mux wiring: serve on the
// public mux, settle on the internal mux, policy learns exactly once.
func TestProductionRouteSettlement(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(envGetter(map[string]string{
		"ROUTER_MODE": "verified", "STRATEGY_ID": "t2",
		"ARMS":           "a,b",
		"EVIDENCE_PATH":  filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH": filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH":  filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN":   "s3cret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	rt, cleanup, err := buildRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	pub, priv := publicMux(rt), internalMux(rt)

	srec := httptest.NewRecorder()
	pub.ServeHTTP(srec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if srec.Code != http.StatusOK {
		t.Fatalf("serve=%d", srec.Code)
	}
	decisionID := srec.Header().Get("X-Decision-ID")
	jobID := srec.Header().Get("X-Job-ID")
	// Settle the arm the decision actually selected: attribution rules
	// reject any other single arm as forged.
	arm := srec.Header().Get("X-Selected-Arm")
	if arm == "" {
		t.Fatal("missing X-Selected-Arm header")
	}
	body, _ := json.Marshal(map[string]any{
		"schema_version": 1, "event_type": "JobSettled",
		"decision_id": decisionID, "job_id": jobID, "strategy_id": "t2",
		"outcome_version": 1, "supersedes": 0, "status": "ACCEPTED",
		"attempts": []map[string]any{{
			"attempt_id": "a1", "seq": 0, "executor_id": arm, "arm_id": arm,
			"transport": "ok", "latency_ms": 120,
			"validation": "pass", "verified": "success", "verified_by": "checker:t",
		}},
		"deciding_attempt_id": "a1",
		"occurred_at":         "2026-09-27T00:00:00Z",
		"verified_by":         "checker:t",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	priv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settle=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["learned"] != true {
		t.Fatalf("not learned: %v", resp)
	}
}

// TestBinaryBootsVerifiedAndSettles builds the actual binary and runs it:
// missing secrets must fail fast; a full serve+settle cycle must learn once.
func TestBinaryBootsVerifiedAndSettles(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test skipped in short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "router-bin")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}

	// Missing SETTLE_TOKEN: fail fast, non-zero exit.
	bad := exec.Command(bin)
	bad.Env = append(os.Environ(),
		"ROUTER_MODE=verified", "ARMS=a",
		"EVIDENCE_PATH="+filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH="+filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH="+filepath.Join(dir, "o.jsonl"),
	)
	if out, err := bad.CombinedOutput(); err == nil {
		t.Fatalf("booted without token: %s", out)
	}

	// Full boot on fixed ports.
	pub, settle := "18081", "18082"
	proc := exec.Command(bin)
	proc.Env = append(os.Environ(),
		"ROUTER_MODE=verified", "STRATEGY_ID=t2", "ARMS=a,b",
		"PORT="+pub, "SETTLE_ADDR=127.0.0.1:"+settle,
		"EVIDENCE_PATH="+filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH="+filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH="+filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN=s3cret",
	)
	proc.Stdout, proc.Stderr = &strings.Builder{}, &strings.Builder{}
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Process.Kill(); _, _ = proc.Process.Wait() }()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := client.Get("http://127.0.0.1:" + pub + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("binary did not become healthy")
		}
		time.Sleep(200 * time.Millisecond)
	}

	sresp, err := client.Post("http://127.0.0.1:"+pub+"/", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("serve=%d", sresp.StatusCode)
	}
	decisionID, jobID := sresp.Header.Get("X-Decision-ID"), sresp.Header.Get("X-Job-ID")
	if decisionID == "" || jobID == "" {
		t.Fatal("missing identity headers")
	}
	// Settle the arm the decision actually selected (attribution rules
	// reject any other single arm as forged).
	arm := sresp.Header.Get("X-Selected-Arm")
	if arm == "" {
		t.Fatal("missing X-Selected-Arm header")
	}
	// Public listener must not settle.
	badSettle, _ := client.Post("http://127.0.0.1:"+pub+"/v1/outcomes", "application/json", strings.NewReader(`{}`))
	badSettle.Body.Close()
	if badSettle.StatusCode != http.StatusNotFound {
		t.Fatalf("public settle=%d want 404", badSettle.StatusCode)
	}
	// Internal listener settles with auth.
	payload := fmt.Sprintf(`{"schema_version":1,"event_type":"JobSettled","decision_id":%q,"job_id":%q,"strategy_id":"t2","outcome_version":1,"supersedes":0,"status":"ACCEPTED","attempts":[{"attempt_id":"a1","seq":0,"executor_id":%q,"arm_id":%q,"transport":"ok","latency_ms":120,"validation":"pass","verified":"success"}],"deciding_attempt_id":"a1","occurred_at":"2026-09-27T00:00:00Z"}`,
		decisionID, jobID, arm, arm)
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+settle+"/v1/outcomes", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer s3cret")
	sresp2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp2.Body.Close()
	if sresp2.StatusCode != http.StatusOK {
		t.Fatalf("settle=%d", sresp2.StatusCode)
	}
	var resp map[string]any
	if err := json.NewDecoder(sresp2.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["learned"] != true {
		t.Fatalf("binary did not learn: %v", resp)
	}
}

// D5: bounded parameters are validated; public settlement needs an explicit
// override; degraded health tested at the gateway layer.
func TestLoadConfigRejectsOutOfRange(t *testing.T) {
	base := map[string]string{"ROUTER_MODE": "verified", "STRATEGY_ID": "t",
		"DECISIONS_PATH": "/tmp/d.jsonl", "OUTCOMES_PATH": "/tmp/o.jsonl", "SETTLE_TOKEN": "s"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for kk, vv := range base {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	for name, kv := range map[string][2]string{
		"shadow-rate-high": {"SHADOW_SAMPLE_RATE", "1.5"},
		"shadow-rate-neg":  {"SHADOW_SAMPLE_RATE", "-0.1"},
		"shadow-conc-neg":  {"SHADOW_MAX_CONCURRENCY", "-2"},
		"shadow-timeout":   {"SHADOW_TIMEOUT", "-5s"},
	} {
		if _, err := loadConfig(envGetter(with(kv[0], kv[1]))); err == nil {
			t.Fatalf("%s: out-of-range value accepted", name)
		}
	}
	// Public settlement listener without override is refused.
	if _, err := loadConfig(envGetter(with("SETTLE_ADDR", "0.0.0.0:8081"))); err == nil {
		t.Fatal("public settlement without override accepted")
	}
	// ... with the explicit override it loads (auth still enforced downstream).
	ok := with("SETTLE_ADDR", "0.0.0.0:8081")
	ok["ALLOW_PUBLIC_SETTLE"] = "1"
	if _, err := loadConfig(envGetter(ok)); err != nil {
		t.Fatalf("override rejected: %v", err)
	}
	// Loopback forms pass.
	for _, addr := range []string{"127.0.0.1:8081", "localhost:8081", "[::1]:8081"} {
		if _, err := loadConfig(envGetter(with("SETTLE_ADDR", addr))); err != nil {
			t.Fatalf("loopback %q rejected: %v", addr, err)
		}
	}
}

// D4: shutdown drains listeners and writes a final checkpoint.
func TestShutdownDrainsAndCheckpoints(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(envGetter(map[string]string{
		"ROUTER_MODE": "verified", "STRATEGY_ID": "t2",
		"ARMS":                "a,b",
		"EVIDENCE_PATH":       filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH":      filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH":       filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN":        "s3cret",
		"CHECKPOINT_INTERVAL": "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	rt, cleanup, err := buildRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Ephemeral listeners: prove drain behavior without fixed ports.
	pubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	privLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &servers{
		public:   &http.Server{Handler: publicMux(rt)},
		internal: &http.Server{Handler: internalMux(rt)},
	}
	go func() { _ = srv.public.Serve(pubLn) }()
	go func() { _ = srv.internal.Serve(privLn) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + pubLn.Addr().String() + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("never healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
	shutdown(srv, cfg, rt, cleanup)
	// Listeners closed...
	if _, err := http.Get("http://" + pubLn.Addr().String() + "/health"); err == nil {
		t.Fatal("public listener still serving after shutdown")
	}
	// ...and a final checkpoint written.
	if _, err := os.Stat(checkpointPath(cfg)); err != nil {
		t.Fatalf("no shutdown checkpoint: %v", err)
	}
	cp, err := outcome.LoadCheckpoint(checkpointPath(cfg))
	if err != nil || cp == nil {
		t.Fatalf("checkpoint unloadable: %+v %v", cp, err)
	}
}
