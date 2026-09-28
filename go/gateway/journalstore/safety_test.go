package journalstore

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

type journalSafetyFixture struct {
	backend *Backend
	router  *gateway.Router
	safety  *gateway.SafetyController
	book    *outcome.CostBookV1
	dir     string
}

func newJournalSafetyFixture(t *testing.T, cfg gateway.SafetyConfig) *journalSafetyFixture {
	t.Helper()
	dir := t.TempDir()
	be, err := OpenBackend(dir, "t3.db")
	if err != nil {
		t.Fatal(err)
	}
	safety, err := gateway.NewSafetyController(cfg, "test-hash", be.Safety(), be.Decisions(), be.Outcomes())
	if err != nil {
		t.Fatal(err)
	}
	q := thompson.NewDefault(cfg.Arms...)
	book := outcome.NewCostBookV1(cfg.Arms)
	cacfg := testCACfg(cfg.QualityFloor)
	cp, err := gateway.NewCostAwarePolicy(q, book, cacfg, safety)
	if err != nil {
		t.Fatal(err)
	}
	reg := gateway.NewProviderRegistry()
	for _, a := range cfg.Arms {
		reg.Register(gateway.NewFakeProvider(a))
	}
	rt, err := gateway.NewRouter(gateway.RouterConfig{
		Policy: cp, Registry: reg, Writer: &gateway.MemoryEvidenceWriter{},
		Decisions: be.Decisions(), Outcomes: be.Outcomes(),
		StrategyID: "t3", Mode: gateway.VerifiedMode,
		SettleAuth: func(r *http.Request) bool { return true },
		CostBook:   book, Safety: safety,
		OperatorAuth: func(r *http.Request) (string, bool) {
			if r.Header.Get("Authorization") == "Bearer op:alice" {
				return "alice", true
			}
			return "", false
		},
		RNGFactory: advancingRNG(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &journalSafetyFixture{backend: be, router: rt, safety: safety, book: book, dir: dir}
}

func (f *journalSafetyFixture) serve(t *testing.T) (did, jid, arm string, code int) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	return rec.Header().Get("X-Decision-ID"), rec.Header().Get("X-Job-ID"), rec.Header().Get("X-Selected-Arm"), rec.Code
}

func (f *journalSafetyFixture) settle(t *testing.T, did, jid, arm string, cost float64, ok bool) int {
	t.Helper()
	ver := outcome.VerifiedSuccess
	st := outcome.StatusAccepted
	if !ok {
		ver, st = outcome.VerifiedFailure, outcome.StatusRejected
	}
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1, Status: st,
		Attempts: []outcome.Attempt{{
			AttemptID: jid + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm,
			Transport: outcome.TransportOK, LatencyMs: 100, CostUSD: &cost,
			Validation: outcome.ValidationPass, Verified: ver, VerifiedBy: "bench",
		}},
		DecidingAttemptID: jid + "-a0", VerifiedBy: "bench", OccurredAt: "2026-01-05T00:00:00Z",
	}
	b, _ := json.Marshal(ev)
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	f.router.SettleHandler(rec, req)
	return rec.Code
}

func safetyTestConfig() gateway.SafetyConfig {
	// Budget sized for a fallback-only test world (MinMeteredN=100 keeps
	// every pick flagged exploration): 200/arm covers establish + collapse
	// + post-suspension serves without the budget backstop firing first.
	// Budget exhaustion itself is covered by dedicated tests.
	return gateway.SafetyConfig{
		Workload: "w", Arms: []string{"cheap", "strong"}, FallbackArm: "strong",
		QualityFloor: 0.5, MonitorWindow: 10, MonitorMinObs: 6,
		MaxExplorationPerArm: 200, MaxMissingShare: 0.2, ColdStartPulls: 5,
	}
}

// testCACfg uses MinMeteredN=100 so no arm ever becomes cost-known inside
// the test: every pick stays fallback max-sample and both arms keep
// receiving traffic by sample variation. This isolates MONITOR behavior
// from the single-sample lock-in of the cost-known optimum path (once any
// arm is cost-known it wins every subsequent pick; documented in the
// pilot limitations). Suspension here comes purely from observed quality.
func testCACfg(floor float64) thompson.CostAwareConfig {
	return thompson.CostAwareConfig{QualityFloor: floor, MinMeteredN: 100, ColdStartPulls: 5, Epsilon: 1e-3}
}

// Deterioration suspends through journal rows; fallback routes; authorized
// resume re-admits; state survives backend reopen. The operator first
// isolates the suspect arm (suspending its sibling), which deterministically
// routes all traffic to it — no seed luck in which arm the policy favors.
func TestJournalSafetyDeterioration(t *testing.T) {
	f := newJournalSafetyFixture(t, safetyTestConfig())
	defer f.backend.Close()
	establish := func(collapseCheap bool) {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			t.Fatalf("serve %d", code)
		}
		c := 0.05
		if arm == "cheap" {
			c = 0.002
		}
		ok := true
		if collapseCheap && arm == "cheap" {
			ok = false
		}
		if code := f.settle(t, did, jid, arm, c, ok); code != 200 {
			t.Fatalf("settle %d", code)
		}
	}
	for i := 0; i < 12; i++ {
		establish(false)
	}
	// Isolate cheap: all subsequent traffic exercises it.
	if err := f.safety.Suspend("operator:test", "strong", "isolate cheap for deterioration watch"); err != nil {
		t.Fatal(err)
	}
	suspended := false
	for i := 0; i < 40 && !suspended; i++ {
		establish(true)
		if st, _, _ := f.safety.State("cheap"); st == gateway.ArmSuspended {
			suspended = true
			t.Logf("suspended after %d collapse jobs", i+1)
		}
	}
	if !suspended {
		t.Fatal("deterioration never triggered suspension")
	}
	// Both arms are now out (cheap monitor-suspended, strong
	// operator-suspended): selection fails closed rather than dispatching
	// unsanctioned traffic.
	if _, _, _, code := f.serve(t); code != 503 {
		t.Fatalf("double suspension must fail closed 503, got %d", code)
	}
	// Authorized operator resume re-admits strong; cheap stays suspended.
	if err := f.safety.Resume("operator:test", "strong", "deterioration watch over"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		_, _, arm, code := f.serve(t)
		if code != 200 || arm != "strong" {
			t.Fatalf("post-resume serve=%d arm=%q", code, arm)
		}
	}
	// Safety rows are journal rows (single authority, no safety.jsonl).
	evs, err := f.backend.Safety().Events()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Type == "ARM_SUSPENDED" && e.Arm == "cheap" {
			found = true
		}
	}
	if !found {
		t.Fatal("suspension event missing from journal")
	}
	// Reopen backend: suspension + budgets replay identically.
	if err := f.backend.Close(); err != nil {
		t.Fatal(err)
	}
	be2, err := OpenBackend(f.dir, "t3.db")
	if err != nil {
		t.Fatal(err)
	}
	defer be2.Close()
	safety2, err := gateway.NewSafetyController(safetyTestConfig(), "test-hash", be2.Safety(), be2.Decisions(), be2.Outcomes())
	if err != nil {
		t.Fatal(err)
	}
	if st, _, _ := safety2.State("cheap"); st != gateway.ArmSuspended {
		t.Fatal("suspension lost across journal reopen")
	}
	if st, _, _ := safety2.State("strong"); st != gateway.ArmPrequalified {
		t.Fatalf("resume lost across journal reopen: %q", st)
	}
}

var safetySeed atomic.Uint64

func advancingRNG() func() *rand.Rand {
	return func() *rand.Rand {
		k := safetySeed.Add(1)
		return rand.New(rand.NewPCG(5+k*101, k))
	}
}
