// Package gateway provides thin-waist HTTP middleware for LLM routing.
//
// Two calls integrate any gateway (LiteLLM, Portkey, custom):
//
//	provider, _ := policy.Select(rng)
//	// forward request to provider
//	policy.RecordOutcome(rng, provider, outcome)
//
// This middleware wraps an http.Handler, selecting a provider per request and
// recording Outcome via thompson.Observer for OTEL/Prometheus.
package gateway

import (
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Middleware selects a provider via Policy and records latency/cost/quality.
// `forward` maps provider id -> upstream URL; return 502 if unknown.
type Middleware struct {
	Policy       *thompson.Policy
	Forward      func(provider string, w http.ResponseWriter, r *http.Request) (thompson.Outcome, error)
	Breaker      *thompson.CircuitBreaker   // per-tenant health, nil disables
	RateLimiter  func(r *http.Request) bool // true = allow, false = 429
	AuthRequired func(r *http.Request) bool // true = authorized
	MaxRetries   int
	// round is a coarse breaker-cooldown clock mutated on every request.
	// Atomic: Handler runs concurrently, a plain uint64 is a data race
	// (`go test -race` flags it under parallel load).
	round atomic.Uint64
}

// Handler returns an http.Handler that does Select -> Forward -> Record.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.AuthRequired != nil && !m.AuthRequired(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if m.RateLimiter != nil && !m.RateLimiter(r) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		if m.Policy == nil {
			http.Error(w, "no policy", http.StatusServiceUnavailable)
			return
		}
		if next != nil && m.Policy.Len() == 0 {
			next.ServeHTTP(w, r)
			return
		}
		rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
		provider, err := m.Policy.Select(rng)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		round := m.round.Load()
		// Health: skip tripped provider
		if m.Breaker != nil && m.Breaker.IsTripped(provider, round) {
			ids := []string{provider}
			if avail := m.Breaker.Available(ids, round); len(avail) > 0 && avail[0] != provider {
				provider = avail[0]
			}
		}
		start := time.Now()
		var outcome thompson.Outcome
		var fwdErr error
		for attempt := 0; attempt <= m.MaxRetries; attempt++ {
			recorder := &ResponseRecorder{ResponseWriter: w, Status: 200}
			outcome, fwdErr = m.Forward(provider, recorder, r)
			if fwdErr == nil && recorder.Status < 500 {
				outcome.Success = recorder.Status < 400
				break
			}
			if attempt < m.MaxRetries {
				jitter := time.Duration(rand.IntN(10)) * time.Millisecond
				time.Sleep(time.Duration(attempt+1)*10*time.Millisecond + jitter)
			}
		}
		m.round.Add(1)
		round = m.round.Load()
		if outcome.LatencyMs == 0 {
			outcome.LatencyMs = float64(time.Since(start).Milliseconds())
		}
		_ = m.Policy.RecordOutcome(rng, provider, outcome)
		if m.Breaker != nil {
			_ = m.Breaker.Record(provider, outcome, round)
		}
		// OtelObserver currently eprintln! `go/thompson/otel.go:1` — swap to opentelemetry.Tracer in prod
		if fwdErr != nil {
			if w.Header().Get("Content-Type") == "" {
				http.Error(w, fwdErr.Error(), http.StatusBadGateway)
			}
			return
		}
	})
}

// NewMiddleware creates a Middleware with default Exact sampler.
func NewMiddleware(policy *thompson.Policy, forward func(string, http.ResponseWriter, *http.Request) (thompson.Outcome, error)) *Middleware {
	return &Middleware{Policy: policy, Forward: forward}
}
