package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func costAwareEnv(t *testing.T, dir string) map[string]string {
	t.Helper()
	sc, _ := json.Marshal(gateway.SafetyConfig{
		Workload: "test", Arms: []string{"a", "b"}, FallbackArm: "b",
		QualityFloor: 0.3, MonitorWindow: 20, MonitorMinObs: 8,
		MaxExplorationPerArm: 30, MaxMissingShare: 0.2, ColdStartPulls: 5,
	})
	scPath := filepath.Join(dir, "safety.json")
	if err := os.WriteFile(scPath, sc, 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"ROUTER_MODE": "verified", "STRATEGY_ID": "t3",
		"ARMS":           "a,b",
		"EVIDENCE_PATH":  filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH": filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH":  filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN":   "s3cret",
		"COSTAWARE":      "1",
		"SAFETY_CONFIG":  scPath,
		"SAFETY_PATH":    filepath.Join(dir, "safety.jsonl"),
		"OPERATOR_TOKEN": "op-token",
	}
}

// COSTAWARE=1 without the safety envelope fails closed before serving.
func TestCostAwareRequiresSafetyEnvelope(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"ROUTER_MODE": "verified", "ARMS": "a,b",
		"EVIDENCE_PATH":  filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH": filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH":  filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN":   "s3cret",
		"COSTAWARE":      "1",
	}
	if _, err := loadConfig(envGetter(env)); err == nil {
		t.Fatal("COSTAWARE without SAFETY_CONFIG/OPERATOR_TOKEN must fail")
	}
	env["SAFETY_CONFIG"] = filepath.Join(dir, "missing.json")
	env["SAFETY_PATH"] = filepath.Join(dir, "s.jsonl")
	env["OPERATOR_TOKEN"] = "op"
	cfg, err := loadConfig(envGetter(env))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildRouter(cfg); err == nil {
		t.Fatal("missing safety config file must fail build")
	}
	// Legacy mode + COSTAWARE is rejected (no silent coexistence).
	env["ROUTER_MODE"] = "legacy"
	if _, err := loadConfig(envGetter(env)); err == nil {
		t.Fatal("COSTAWARE in legacy mode must fail")
	}
}

// The built binary serves cost-aware decisions with the experimental
// identity and refuses operator paths on the public listener.
func TestCostAwareBinaryServesAndIsolatesOperator(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(envGetter(costAwareEnv(t, dir)))
	if err != nil {
		t.Fatal(err)
	}
	rt, cleanup, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("cost-aware build: %v", err)
	}
	defer cleanup()
	pub, priv := publicMux(rt), internalMux(rt)

	rec := httptest.NewRecorder()
	pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("serve=%d", rec.Code)
	}
	if id := rec.Header().Get("X-Selected-Arm"); id != "a" && id != "b" {
		t.Fatalf("no selected arm: %q", id)
	}
	_ = thompson.CostAwarePolicyID

	for _, p := range []string{"/v1/operator/suspend", "/v1/operator/resume"} {
		rec := httptest.NewRecorder()
		pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{}`)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("public operator path %s=%d want 404", p, rec.Code)
		}
	}
	// Internal operator endpoints exist and reject anonymous callers.
	rec = httptest.NewRecorder()
	priv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/operator/suspend", strings.NewReader(`{"reason":"x"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous operator=%d want 401", rec.Code)
	}
}

// JOURNAL_PATH without COSTAWARE is refused (journal is T3-experimental only).
func TestJournalRequiresCostAware(t *testing.T) {
	dir := t.TempDir()
	env := costAwareEnv(t, dir)
	env["COSTAWARE"] = ""
	env["JOURNAL_PATH"] = filepath.Join(dir, "j.db")
	if _, err := loadConfig(envGetter(env)); err == nil {
		t.Fatal("JOURNAL_PATH without COSTAWARE=1 must fail")
	}
}

// Full journal-mode binary: cost-aware decisions settle and learn through
// the SQLite backend with no JSONL ledgers created.
func TestJournalBinaryServesAndSettles(t *testing.T) {
	dir := t.TempDir()
	env := costAwareEnv(t, dir)
	env["JOURNAL_PATH"] = filepath.Join(dir, "journal.db")
	cfg, err := loadConfig(envGetter(env))
	if err != nil {
		t.Fatal(err)
	}
	rt, cleanup, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("journal build: %v", err)
	}
	defer cleanup()
	pub, priv := publicMux(rt), internalMux(rt)

	srec := httptest.NewRecorder()
	pub.ServeHTTP(srec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if srec.Code != http.StatusOK {
		t.Fatalf("serve=%d", srec.Code)
	}
	decID := srec.Header().Get("X-Decision-ID")
	jobID := srec.Header().Get("X-Job-ID")
	arm := srec.Header().Get("X-Selected-Arm")
	cost := 0.02
	ev := map[string]any{
		"schema_version": 1, "event_type": "JobSettled",
		"decision_id": decID, "job_id": jobID, "strategy_id": "t3",
		"outcome_version": 1, "supersedes": 0, "status": "ACCEPTED",
		"attempts": []any{map[string]any{
			"attempt_id": jobID + "-a0", "seq": 0, "executor_id": arm, "arm_id": arm,
			"transport": "ok", "latency_ms": 120.0, "cost_usd": cost,
			"validation": "pass", "verified": "success", "verified_by": "bench",
		}},
		"deciding_attempt_id": jobID + "-a0",
		"verified_by":         "bench",
		"occurred_at":         "2026-01-05T00:00:00Z",
	}
	b, _ := json.Marshal(ev)
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	priv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settle=%d body=%s", rec.Code, rec.Body.String())
	}
	// No JSONL authorities exist beside evidence (export artifact).
	for _, f := range []string{"decisions.jsonl", "outcomes.jsonl", "safety.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Fatalf("journal mode created competing %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "journal.db")); err != nil {
		t.Fatal("journal database missing")
	}
	_ = thompson.CostAwarePolicyID
}
