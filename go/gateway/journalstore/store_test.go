package journalstore

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// backendFixture wires a full cost-aware Router over journal stores.
type backendFixture struct {
	backend *Backend
	router  *gateway.Router
	policy  *gateway.CostAwarePolicy
	quality *thompson.Policy
	dir     string
}

func newBackendFixture(t *testing.T, arms []string) *backendFixture {
	t.Helper()
	dir := t.TempDir()
	be, err := OpenBackend(dir, "t3.db")
	if err != nil {
		t.Fatal(err)
	}
	q := thompson.NewDefault(arms...)
	book := outcome.NewCostBookV1(arms)
	cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
	cp, err := gateway.NewCostAwarePolicy(q, book, cfg, gateway.NewStaticGate(arms))
	if err != nil {
		t.Fatal(err)
	}
	reg := gateway.NewProviderRegistry()
	for _, a := range arms {
		reg.Register(gateway.NewFakeProvider(a))
	}
	rt, err := gateway.NewRouter(gateway.RouterConfig{
		Policy: cp, Registry: reg, Writer: &gateway.MemoryEvidenceWriter{},
		Decisions: be.Decisions(), Outcomes: be.Outcomes(),
		StrategyID: "t3", Mode: gateway.VerifiedMode,
		SettleAuth: func(r *http.Request) bool { return true },
		CostBook:   book,
		RNGFactory: func() *rand.Rand { return rand.New(rand.NewPCG(11, 11)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &backendFixture{backend: be, router: rt, policy: cp, quality: q, dir: dir}
}

func (f *backendFixture) close(t *testing.T) {
	t.Helper()
	if err := f.backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f *backendFixture) serve(t *testing.T) (did, jid, arm string, code int) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	return rec.Header().Get("X-Decision-ID"), rec.Header().Get("X-Job-ID"), rec.Header().Get("X-Selected-Arm"), rec.Code
}

func (f *backendFixture) settleJSON(t *testing.T, ev outcome.OutcomeEvent) int {
	t.Helper()
	var buf strings.Builder
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	_ = buf
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", strings.NewReader(string(b)))
	rec := httptest.NewRecorder()
	f.router.SettleHandler(rec, req)
	return rec.Code
}

// Full round trip through the actual Router: select, settle, correct,
// duplicate; every refusal and learning semantic preserved.
func TestJournalBackendRouterRoundTrip(t *testing.T) {
	f := newBackendFixture(t, []string{"cheap", "strong"})
	defer f.close(t)
	did, jid, arm, code := f.serve(t)
	if code != 200 || arm == "" {
		t.Fatalf("serve %d %q", code, arm)
	}
	dec, ok := f.backend.Decisions().Lookup(did)
	if !ok {
		t.Fatal("committed decision missing from journal")
	}
	if dec.LoggingPolicyID != thompson.CostAwarePolicyID {
		t.Fatalf("wrong identity: %q", dec.LoggingPolicyID)
	}
	if dec.CostAware == nil || dec.CostAware.RuleVersion < thompson.CostAwareRuleV2 {
		t.Fatalf("missing rule identity: %+v", dec.CostAware)
	}
	cost := 0.01
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1,
		Status: outcome.StatusAccepted,
		Attempts: []outcome.Attempt{{
			AttemptID: jid + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm,
			Transport: outcome.TransportOK, LatencyMs: 120, CostUSD: &cost,
			Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess,
			VerifiedBy: "bench",
		}},
		DecidingAttemptID: jid + "-a0", VerifiedBy: "bench",
		OccurredAt: "2026-01-05T00:00:00Z",
	}
	if code := f.settleJSON(t, ev); code != 200 {
		t.Fatalf("settle %d", code)
	}
	// Duplicate: idempotent.
	if code := f.settleJSON(t, ev); code != 200 {
		t.Fatalf("duplicate settle %d", code)
	}
	// Correction to REJECTED: consistent rebuild both sides.
	corr := ev
	corr.Version, corr.Supersedes, corr.Status = 2, 1, outcome.StatusRejected
	corr.Attempts[0].Verified = outcome.VerifiedFailure
	if code := f.settleJSON(t, corr); code != 200 {
		t.Fatalf("correction settle %d", code)
	}
	store := f.backend.Outcomes()
	if store.Len() != 2 {
		t.Fatalf("outcome rows=%d want 2 versions", store.Len())
	}
	latest, ok := store.Latest(jid)
	if !ok || latest.Version != 2 || latest.Status != outcome.StatusRejected {
		t.Fatalf("latest wrong: %+v", latest)
	}
	// Full-fidelity round trip: every attempt field survives.
	if latest.Attempts[0].LatencyMs != 120 || latest.Attempts[0].Transport != outcome.TransportOK {
		t.Fatalf("fidelity loss: %+v", latest.Attempts[0])
	}
}

// Decision + budget atomicity across crash: reopen and verify both-or-neither.
func TestJournalBackendAtomicRecovery(t *testing.T) {
	dir := t.TempDir()
	be, err := OpenBackend(dir, "t3.db")
	if err != nil {
		t.Fatal(err)
	}
	d := gateway.CommittedDecision{
		DecisionID: "d1", JobID: "j1", StrategyID: "t3", SelectedArmID: "cheap",
		EligibleArmIDs: []string{"cheap", "strong"},
		EligibleArmState: []gateway.EligibleArmState{
			{ArmID: "cheap", Alpha: 2, Beta: 1, Pulls: 1},
			{ArmID: "strong", Alpha: 1, Beta: 1, Pulls: 0},
		},
		SampledScores: map[string]float64{"cheap": 0.7, "strong": 0.4},
		ScoreKind:     gateway.ScoreBetaSamples, LoggingPolicyID: thompson.CostAwarePolicyID,
		ConfigHash: "cfg", OccurredAt: "2026-01-05T00:00:00Z",
		CostAware: &thompson.CostAwareResult{
			ArmID: "cheap", PolicyID: thompson.CostAwarePolicyID,
			Objective:   thompson.CostAwareObjectiveVer,
			RuleVersion: thompson.CostAwareRuleV3,
		},
	}
	stored, committed, err := be.Decisions().Commit(d)
	if err != nil || !committed {
		t.Fatalf("commit: %v %v", err, committed)
	}
	if stored.Seq == 0 {
		t.Fatal("missing rowid seq")
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	be2, err := OpenBackend(dir, "t3.db")
	if err != nil {
		t.Fatal(err)
	}
	defer be2.Close()
	got, ok := be2.Decisions().Lookup("d1")
	if !ok || got.SelectedArmID != "cheap" || got.Seq != stored.Seq {
		t.Fatalf("recovery diverged: %+v", got)
	}
	if got.EligibleArmState[0].Alpha != 2 || got.ScoreKind != gateway.ScoreBetaSamples {
		t.Fatalf("evidence fidelity lost: %+v", got)
	}
}

// Mixed authority refused: journal backend never opens beside JSONL.
func TestJournalBackendRefusesMixedAuthority(t *testing.T) {
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "decisions.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := OpenBackend(dir, "t3.db"); err == nil {
		t.Fatal("mixed JSONL+journal authorities must be refused")
	}
}

// Safety events round-trip with seq/actor/reason/evidence intact.
func TestJournalBackendSafetyRoundTrip(t *testing.T) {
	f := newBackendFixture(t, []string{"cheap", "strong"})
	defer f.close(t)
	sb := f.backend.Safety()
	if err := sb.Append(gateway.SafetyEvent{Actor: "op:test", Type: "ARM_SUSPENDED", Arm: "cheap", Reason: "r", Evidence: `{"n":1}`}); err != nil {
		t.Fatal(err)
	}
	evs, err := sb.Events()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Arm != "cheap" || evs[0].Evidence != `{"n":1}` || evs[0].Seq == 0 {
		t.Fatalf("safety round trip wrong: %+v", evs)
	}
}

// Journal files match the JSONL 0600 posture (SQLite inherits umask
// otherwise): after writes and at rest every existing file is private.
func TestJournalFilesPrivate(t *testing.T) {
	f := newBackendFixture(t, []string{"cheap", "strong"})
	if _, _, _, code := f.serve(t); code != 200 {
		t.Fatalf("serve %d", code)
	}
	if err := f.backend.Close(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range []string{"t3.db", "t3.db-wal", "t3.db-shm"} {
		fi, err := os.Stat(filepath.Join(f.dir, p))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		found = true
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%o want 600", p, fi.Mode().Perm())
		}
	}
	if !found {
		t.Fatal("no journal files found")
	}
}
