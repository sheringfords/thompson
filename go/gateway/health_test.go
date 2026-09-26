package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Health is 200 on a fresh router and 503 within a minute of a request-path
// persistence failure (fail-closed requests already 500; health lets balancers drain).
func TestHealthDegradedAfterPersistenceFailure(t *testing.T) {
	policy := thompson.NewDefault("a")
	mem := &MemoryEvidenceWriter{}
	store := &failCommitStore{err: errors.New("disk gone")}
	reg := NewProviderRegistry()
	reg.Register(NewFakeProvider("a"))
	rt, err := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		Decisions: store,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	rt.HealthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh health=%d want 200", rec.Code)
	}

	srec := httptest.NewRecorder()
	rt.ServeHTTP(srec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if srec.Code != http.StatusInternalServerError {
		t.Fatalf("serve=%d want 500", srec.Code)
	}

	drec := httptest.NewRecorder()
	rt.HealthHandler(drec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if drec.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded health=%d want 503", drec.Code)
	}
	if !strings.Contains(drec.Body.String(), "degraded") {
		t.Fatalf("body=%q", drec.Body.String())
	}
}
