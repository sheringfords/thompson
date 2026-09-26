package gateway

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// decisionIDKey is the context key for decision_id propagation.
type decisionIDKey struct{}

// DecisionIDFromContext returns the decision_id stored in ctx, if any.
func DecisionIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(decisionIDKey{}).(string)
	return v, ok
}

// Router is the deployable routing path: Select -> persist -> Execute -> Reward -> Record -> persist (+ optional shadow).
// Single Policy instance per process (state_ownership requirement), guarded by policy's own mutex.
type Router struct {
	policy     *thompson.Policy
	registry   *ProviderRegistry
	writer     EvidenceWriter
	decisions  DecisionStore
	strategyID string
	mode       RouterMode
	outcomes   outcome.OutcomeStore
	learner    *outcome.Learner
	mapper     outcome.RewardMapper
	settleAuth func(r *http.Request) bool
	settleMu   sync.Mutex
	// persistIssue holds the Unix-nano timestamp of the last request-path
	// persistence failure (decision commit, evidence write). HealthHandler
	// reports degraded within a minute of it.
	persistIssue  atomic.Int64
	rngFactory    func() *rand.Rand
	mu            sync.Mutex
	recorded      map[string]bool
	eligibility   ShadowEligibility
	shadowState   *shadowState
	shadowMetrics shadowMetrics
}

// RouterMode selects the learning contract. LegacyMode preserves the
// transport-derived Record path. VerifiedMode disables it: the request path
// records transport observation only and learns exclusively through versioned
// settlement. The two modes never both update the policy for one decision.
type RouterMode string

const (
	// LegacyMode learns from transport-derived reward in the request path.
	LegacyMode RouterMode = "legacy"
	// VerifiedMode learns only from settled, independently verified outcomes.
	VerifiedMode RouterMode = "verified"
)

type RouterConfig struct {
	Policy   *thompson.Policy
	Registry *ProviderRegistry
	Writer   EvidenceWriter
	// Decisions persists committed decisions before execution. Nil defaults
	// to an in-memory store (legacy-compatible, non-durable).
	Decisions DecisionStore
	// StrategyID names the execution strategy this router instantiates.
	// Empty defaults to "default".
	StrategyID string
	// Mode selects legacy vs verified learning. Empty defaults to legacy.
	Mode RouterMode
	// Outcomes is the durable store for settled job outcomes (verified mode
	// only; must be explicitly provided, never defaulted).
	Outcomes outcome.OutcomeStore
	// Learner folds settled outcomes into Policy (verified mode only).
	// Nil constructs one over Policy with Mapper (or the binary default).
	Learner *outcome.Learner
	// Mapper converts settled jobs to rewards. Nil means the binary status
	// default. Rejected in legacy mode.
	Mapper outcome.RewardMapper
	// SettleAuth authorizes POST /v1/outcomes. Required in verified mode:
	// outcome writes are never unauthenticated.
	SettleAuth           func(r *http.Request) bool
	RNGFactory           func() *rand.Rand
	ShadowEligibility    ShadowEligibility
	ShadowSampleRate     float64
	ShadowTimeout        time.Duration
	ShadowMaxConcurrency int
	ShadowMaxBodyBytes   int64
	ShadowRNGSeed        uint64
}

func NewRouter(cfg RouterConfig) (*Router, error) {
	if cfg.Policy == nil {
		return nil, fmt.Errorf("router: policy is nil")
	}
	if cfg.Registry == nil {
		return nil, fmt.Errorf("router: registry is nil")
	}
	if cfg.Writer == nil {
		return nil, fmt.Errorf("router: evidence writer is nil")
	}
	factory := cfg.RNGFactory
	if factory == nil {
		factory = func() *rand.Rand {
			return rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano()>>32)))
		}
	}
	elig := cfg.ShadowEligibility
	if elig == nil {
		elig = DenyAllEligibility{}
	}
	shadowCfg := ShadowConfig{
		SampleRate:     cfg.ShadowSampleRate,
		Timeout:        cfg.ShadowTimeout,
		MaxConcurrency: cfg.ShadowMaxConcurrency,
		MaxBodyBytes:   cfg.ShadowMaxBodyBytes,
		ShadowRNGSeed:  cfg.ShadowRNGSeed,
	}
	decisions := cfg.Decisions
	if decisions == nil {
		decisions = NewMemoryDecisionStore()
	}
	strategy := cfg.StrategyID
	if strategy == "" {
		strategy = "default"
	}
	mode := cfg.Mode
	if mode == "" {
		mode = LegacyMode
	}
	if mode != LegacyMode && mode != VerifiedMode {
		return nil, fmt.Errorf("router: unknown mode %q", string(mode))
	}
	rt := &Router{
		policy:      cfg.Policy,
		registry:    cfg.Registry,
		writer:      cfg.Writer,
		decisions:   decisions,
		strategyID:  strategy,
		mode:        mode,
		rngFactory:  factory,
		recorded:    make(map[string]bool),
		eligibility: elig,
		shadowState: newShadowState(shadowCfg),
	}
	if err := rt.initVerifiedSettlement(cfg); err != nil {
		return nil, err
	}
	return rt, nil
}

func (rt *Router) SetShadowSampleRate(rate float64) { rt.shadowState.setSampleRate(rate) }
func (rt *Router) GetShadowSampleRate() float64     { return rt.shadowState.getSampleRate() }
func (rt *Router) ShadowMetrics() (attempted, sampled, executed, timeout, rateLimited uint64) {
	return rt.shadowMetrics.attempted.Load(), rt.shadowMetrics.sampled.Load(), rt.shadowMetrics.executed.Load(), rt.shadowMetrics.timeout.Load(), rt.shadowMetrics.rateLimited.Load()
}

func generateCanonicalID() string {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixNano()>>1)
	}
	return hex.EncodeToString(b[:])
}

func extractExternalID(r *http.Request) *string {
	if v := r.Header.Get("X-Decision-ID"); v != "" {
		if len(v) >= 8 && len(v) <= 128 {
			ok := true
			for _, c := range v {
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
					ok = false
					break
				}
			}
			if ok {
				s := v
				return &s
			}
		}
	}
	if v := r.Header.Get("X-Request-ID"); v != "" {
		if len(v) >= 8 && len(v) <= 128 {
			s := v
			return &s
		}
	}
	return nil
}

func (rt *Router) isAlreadyRecorded(id string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.recorded[id]
}
func (rt *Router) markRecorded(id string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.recorded[id] = true
}

func computeReward(base thompson.RewardPolicy, latency float64, success bool, cost *float64) float64 {
	rp := base
	if cost == nil {
		rp.Weights.Cost = 0
	}
	costVal := 0.0
	if cost != nil {
		costVal = *cost
	}
	o := thompson.Outcome{LatencyMs: latency, Success: success, CostUSD: costVal}
	return rp.Reward(o)
}

// ServeHTTP implements V1 integrity order:
// Select -> DecisionStarted -> Primary Execute -> ExecutionObserved -> Reward -> Record -> DecisionLearned -> Shadow (or Skipped)
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	canonicalID := generateCanonicalID()
	externalID := extractExternalID(r)
	ctx := context.WithValue(r.Context(), decisionIDKey{}, canonicalID)
	r = r.WithContext(ctx)
	w.Header().Set("X-Decision-ID", canonicalID)
	if externalID != nil {
		w.Header().Set("X-External-Request-ID", *externalID)
	}

	// Atomic decision snapshot (Phase 3/P0 fix for audit A1): eligible set,
	// selection, sampled scores, per-arm posteriors, total pulls and config
	// all come from one locked instant. Separate EligibleArmIDs /
	// SelectWithScores / PosteriorFor / ConfigHash calls could observe
	// different policy versions under concurrency and tear the evidence.
	rng := rt.rngFactory()
	snap, err := rt.policy.SelectSnapshot(rng)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	eligible := snap.Eligible
	if len(eligible) == 0 {
		http.Error(w, "no arms registered", http.StatusServiceUnavailable)
		return
	}
	chosen := snap.Selected
	sampledScores := snap.Scores
	postBefore, ok := snap.Posteriors[chosen]
	if !ok {
		http.Error(w, "selected arm not found", http.StatusInternalServerError)
		return
	}
	configHash := snap.ConfigHash
	jobID := "job-" + canonicalID
	w.Header().Set("X-Job-ID", jobID)

	// Shadow eligibility & sampling (isolated RNG) - provisional before body size check
	shadowEligible := rt.eligibility.IsEligible(r)
	shadowSampled := false
	var shadowArmID *string
	var shadowReason string
	var eligibleCount, shadowCandidateCount int
	eligibleCount = len(eligible)
	shadowCandidateCount = len(eligible) - 1
	if shadowCandidateCount < 0 {
		shadowCandidateCount = 0
	}
	if !shadowEligible {
		shadowReason = "NOT_ELIGIBLE"
	} else if rt.shadowState.getSampleRate() <= 0 {
		shadowReason = "KILL_SWITCH"
	} else {
		rate := rt.shadowState.getSampleRate()
		if !rt.shadowState.sampler.shouldSample(rate) {
			shadowReason = "NOT_SAMPLED"
		} else {
			eligibleCopy := make([]string, len(eligible))
			copy(eligibleCopy, eligible)
			rt.shadowState.mu.Lock()
			arm, ok := selectShadowArm(eligibleCopy, chosen, rt.shadowState.shadowRNG)
			rt.shadowState.mu.Unlock()
			if !ok {
				shadowReason = "NO_ALTERNATE_ARM"
			} else {
				shadowSampled = true
				s := arm
				shadowArmID = &s
				shadowReason = ""
			}
		}
	}

	// Build eligible arm state snapshot for OPE (deterministic order).
	// Sourced from the atomic decision snapshot, so the persisted posteriors
	// are exactly the state the Thompson draw ran on.
	eligibleState := make([]EligibleArmState, 0, len(eligible))
	for _, id := range eligible {
		p := snap.Posteriors[id]
		eligibleState = append(eligibleState, EligibleArmState{ArmID: id, Alpha: p.Alpha, Beta: p.Beta, Pulls: p.Pulls})
	}
	// LoggingPolicyID is derived from the live configuration, never hardcoded
	// (audit A3): only exact-Thompson passes the OPE reliability gate.
	loggingPolicyID := rt.policy.LoggingPolicyID()
	// Durable decision commit (PR 3A): the immutable identity + selection
	// evidence is persisted BEFORE any external execution begins. Commit
	// failure is fail-closed: no provider is dispatched.
	_, _, err = rt.decisions.Commit(CommittedDecision{
		DecisionID:       canonicalID,
		JobID:            jobID,
		StrategyID:       rt.strategyID,
		SelectedArmID:    chosen,
		EligibleArmIDs:   eligible,
		EligibleArmState: eligibleState,
		SampledScores:    sampledScores,
		ScoreKind:        scoreKindFor(snap.Config.Selection.Kind, snap.Forced),
		LoggingPolicyID:  loggingPolicyID,
		ConfigHash:       configHash,
		OccurredAt:       nowRFC3339Nano(),
	})
	if err != nil {
		rt.notePersistenceIssue()
		http.Error(w, fmt.Sprintf("decision commit failed: %v", err), http.StatusInternalServerError)
		return
	}
	if err := rt.decisions.MarkExecution(DecisionExecution{
		DecisionID: canonicalID, Phase: PhaseDispatched, OccurredAt: nowRFC3339Nano(),
	}); err != nil {
		rt.notePersistenceIssue()
		http.Error(w, fmt.Sprintf("decision dispatch mark failed: %v", err), http.StatusInternalServerError)
		return
	}
	// Verified-mode clients build attempt tapes from live executions and need
	// the selected arm without re-reading the ledger.
	w.Header().Set("X-Selected-Arm", chosen)
	started := DecisionStarted{
		SchemaVersion:           1,
		EventType:               "DecisionStarted",
		DecisionID:              canonicalID,
		OccurredAt:              nowRFC3339Nano(),
		EligibleArmIDs:          eligible,
		SelectedArmID:           chosen,
		SampledScores:           sampledScores,
		PolicyConfigHash:        configHash,
		PosteriorBefore:         snapshotFrom(postBefore),
		EligibleArmState:        eligibleState,
		LoggingPolicyID:         loggingPolicyID,
		LoggingPolicyConfigHash: configHash,
		ExternalRequestID:       externalID,
		JobID:                   jobID,
		StrategyID:              rt.strategyID,
		ShadowEligible:          shadowEligible,
		ShadowSampled:           shadowSampled,
		ShadowArmID:             shadowArmID,
	}
	if err := rt.writer.WriteDecisionStarted(started); err != nil {
		rt.notePersistenceIssue()
		http.Error(w, fmt.Sprintf("evidence write failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Buffer request body PRESERVING primary: read fully, then check shadow limit
	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		bodyBytes = b
		_ = r.Body.Close()
		// Shadow body-size gate: does not truncate primary, only disables shadow
		limit := rt.shadowState.config.MaxBodyBytes
		if limit == 0 {
			limit = MaxBodyBytes
		}
		if shadowSampled && int64(len(bodyBytes)) > limit {
			// Preserve primary body byte-for-byte; suppress shadow only
			shadowReason = "BODY_TOO_LARGE"
			shadowSampled = false
			// Keep shadowArmID for Skipped intended, but shadow will not execute due to sampled false
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Primary execution
	provider, ok := rt.registry.Get(chosen)
	var provOutcome ProviderOutcome
	var execErr error
	start := time.Now()
	if !ok {
		execErr = fmt.Errorf("no provider for arm %q", chosen)
		provOutcome = ProviderOutcome{Success: false, StatusCode: 502}
	} else {
		provOutcome, execErr = provider.Invoke(ctx, r)
		_ = execErr
	}
	latencyMs := float64(time.Since(start).Nanoseconds()) / 1e6
	success := provOutcome.Success
	if execErr != nil {
		success = false
	}

	if provOutcome.ResponseHeader != nil {
		for k, vv := range provOutcome.ResponseHeader {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
	}
	if provOutcome.StatusCode != 0 && w.Header().Get("Content-Type") == "" {
		w.WriteHeader(provOutcome.StatusCode)
	} else if provOutcome.StatusCode == 0 {
		if success {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
	}
	if len(provOutcome.ResponseBody) > 0 {
		_, _ = w.Write(provOutcome.ResponseBody)
	} else if execErr != nil {
		_, _ = w.Write([]byte(execErr.Error()))
	}

	observed := ExecutionObserved{
		SchemaVersion: 1,
		EventType:     "ExecutionObserved",
		DecisionID:    canonicalID,
		OccurredAt:    nowRFC3339Nano(),
		ArmID:         chosen,
		LatencyMs:     latencyMs,
		Success:       success,
		InputTokens:   provOutcome.InputTokens,
		OutputTokens:  provOutcome.OutputTokens,
		CostUSD:       provOutcome.CostUSD,
	}
	if err := rt.writer.WriteExecutionObserved(observed); err != nil {
		rt.notePersistenceIssue()
		http.Error(w, fmt.Sprintf("evidence write failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Execution-progress mark (best-effort post-execution: the caller already
	// has a response, and ExecutionObserved above is the durable observation).
	// A transport error means the provider never rendered a verdict, so the
	// execution outcome stays unknown until settlement resolves it.
	execPhase := PhaseObserved
	execTransport := "ok"
	if !success {
		execTransport = "error"
	}
	if execErr != nil {
		execPhase = PhaseUnknown
		execTransport = "transport_error"
	}
	lat := latencyMs
	_ = rt.decisions.MarkExecution(DecisionExecution{
		DecisionID: canonicalID, Phase: execPhase, Transport: execTransport,
		LatencyMs: &lat, OccurredAt: nowRFC3339Nano(),
	})

	// Primary learning BEFORE shadow (V1 integrity). In verified mode this
	// whole block is skipped: transport observation only, learning happens
	// exclusively through versioned settlement (see SettleHandler).
	if rt.mode != VerifiedMode {
		if rt.isAlreadyRecorded(canonicalID) {
			http.Error(w, "duplicate record for decision", http.StatusInternalServerError)
			return
		}
		reward := computeReward(snap.Config.Reward, latencyMs, success, provOutcome.CostUSD)
		if err := rt.policy.Record(rng, chosen, reward); err != nil {
			http.Error(w, fmt.Sprintf("record failed: %v", err), http.StatusInternalServerError)
			return
		}
		rt.markRecorded(canonicalID)
		postAfter, _ := rt.policy.PosteriorFor(chosen)
		totalAfter := rt.policy.TotalPulls()
		learned := DecisionLearned{
			SchemaVersion:   1,
			EventType:       "DecisionLearned",
			DecisionID:      canonicalID,
			OccurredAt:      nowRFC3339Nano(),
			ArmID:           chosen,
			ComputedReward:  reward,
			PosteriorBefore: snapshotFrom(postBefore),
			PosteriorAfter:  snapshotFrom(postAfter),
			TotalPullsAfter: totalAfter,
		}
		if err := rt.writer.WriteDecisionLearned(learned); err != nil {
			_ = err
			return
		}
	} // end legacy transport-derived learning (skipped in verified mode)

	// Shadow execution only after live learning is complete
	if shadowSampled && shadowArmID != nil {
		rt.shadowMetrics.attempted.Add(1)
		rt.shadowMetrics.sampled.Add(1)
		if !rt.shadowState.budget.TryAcquire() {
			rt.shadowMetrics.rateLimited.Add(1)
			// Emit Skipped with CONCURRENCY_BUDGET
			prob := 0.0
			if shadowCandidateCount > 0 {
				prob = 1.0 / float64(shadowCandidateCount)
			}
			p := prob
			_ = rt.writer.WriteShadowSkipped(ShadowSkipped{
				SchemaVersion: 1, EventType: "ShadowSkipped", DecisionID: canonicalID, OccurredAt: nowRFC3339Nano(),
				IntendedShadowArmID: shadowArmID, Reason: "CONCURRENCY_BUDGET",
				PrimaryLoggingPolicyID: rt.policy.LoggingPolicyID(), PrimaryLoggingPolicyConfigHash: configHash,
				ShadowSelectionPolicyID: "uniform-non-primary-v1", ShadowSelectionProbability: &p,
				EligibleArmCount: eligibleCount, ShadowCandidateCount: shadowCandidateCount,
			})
		} else {
			func() {
				defer rt.shadowState.budget.Release()
				shadowProvider, ok := rt.registry.Get(*shadowArmID)
				if !ok {
					prob := 0.0
					if shadowCandidateCount > 0 {
						prob = 1.0 / float64(shadowCandidateCount)
					}
					p := prob
					_ = rt.writer.WriteShadowSkipped(ShadowSkipped{
						SchemaVersion: 1, EventType: "ShadowSkipped", DecisionID: canonicalID, OccurredAt: nowRFC3339Nano(),
						IntendedShadowArmID: shadowArmID, Reason: "PROVIDER_UNAVAILABLE",
						PrimaryLoggingPolicyID: rt.policy.LoggingPolicyID(), PrimaryLoggingPolicyConfigHash: configHash,
						ShadowSelectionPolicyID: "uniform-non-primary-v1", ShadowSelectionProbability: &p,
						EligibleArmCount: eligibleCount, ShadowCandidateCount: shadowCandidateCount,
					})
					return
				}
				shadowReq := r.Clone(context.Background())
				shadowReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				shadowTimeout := rt.shadowState.getTimeout()
				sCtx, cancel := context.WithTimeout(context.Background(), shadowTimeout)
				defer cancel()
				shadowReq = shadowReq.WithContext(sCtx)
				sStart := time.Now()
				sOutcome, sErr := shadowProvider.Invoke(sCtx, shadowReq)
				sLatency := float64(time.Since(sStart).Nanoseconds()) / 1e6
				sSuccess := sOutcome.Success
				if sErr != nil {
					sSuccess = false
					if sCtx.Err() == context.DeadlineExceeded {
						rt.shadowMetrics.timeout.Add(1)
					}
				}
				sReward := computeReward(snap.Config.Reward, sLatency, sSuccess, sOutcome.CostUSD)
				prob := 0.0
				if shadowCandidateCount > 0 {
					prob = 1.0 / float64(shadowCandidateCount)
				}
				ev := ShadowExecutionObserved{
					SchemaVersion: 1, EventType: "ShadowExecutionObserved", DecisionID: canonicalID, OccurredAt: nowRFC3339Nano(),
					ArmID: *shadowArmID, PrimaryArmID: chosen, LatencyMs: sLatency, Success: sSuccess,
					InputTokens: sOutcome.InputTokens, OutputTokens: sOutcome.OutputTokens, CostUSD: sOutcome.CostUSD,
					ComputedReward:         sReward,
					PrimaryLoggingPolicyID: rt.policy.LoggingPolicyID(), PrimaryLoggingPolicyConfigHash: configHash,
					ShadowSelectionPolicyID: "uniform-non-primary-v1", ShadowSelectionProbability: prob,
					EligibleArmCount: eligibleCount, ShadowCandidateCount: shadowCandidateCount,
				}
				_ = rt.writer.WriteShadowExecutionObserved(ev)
				rt.shadowMetrics.executed.Add(1)
			}()
		}
	} else if shadowReason == "BODY_TOO_LARGE" {
		// Body too large suppressed after sampling: emit Skipped to fix stale DecisionStarted
		var intended *string
		if shadowArmID != nil {
			// shadowArmID was nilled after body check, but we lost intended; reconstruct if needed
			// For BODY_TOO_LARGE we had an intended arm before, but nilled. For V1 we treat intended as nil with reason.
		}
		var probPtr *float64
		if shadowCandidateCount > 0 {
			p := 1.0 / float64(shadowCandidateCount)
			probPtr = &p
		}
		_ = rt.writer.WriteShadowSkipped(ShadowSkipped{
			SchemaVersion: 1, EventType: "ShadowSkipped", DecisionID: canonicalID, OccurredAt: nowRFC3339Nano(),
			IntendedShadowArmID: intended, Reason: shadowReason,
			PrimaryLoggingPolicyID: rt.policy.LoggingPolicyID(), PrimaryLoggingPolicyConfigHash: configHash,
			ShadowSelectionPolicyID: "uniform-non-primary-v1", ShadowSelectionProbability: probPtr,
			EligibleArmCount: eligibleCount, ShadowCandidateCount: shadowCandidateCount,
		})
	}
}

// notePersistenceIssue records a request-path persistence failure for the
// degraded-health window. Call on decision-commit and evidence-write
// failures only; validation rejections are client errors.
func (rt *Router) notePersistenceIssue() {
	rt.persistIssue.Store(time.Now().UnixNano())
}

func (rt *Router) HealthHandler(w http.ResponseWriter, r *http.Request) {
	// Liveness with a degraded window: the process refuses to start unless
	// required storage opens, and request-time persistence failures already
	// fail closed per request. A persistence failure within the last minute
	// additionally marks the instance unhealthy so load balancers drain it.
	// (Scope: decision-commit and evidence-write failures on this router's
	// request path. Settlement validation rejections are client errors, not
	// storage failures, and do not affect health.)
	w.Header().Set("Content-Type", "application/json")
	if nanos := rt.persistIssue.Load(); nanos != 0 && time.Since(time.Unix(0, nanos)) < time.Minute {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
