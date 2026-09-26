// Router binary — deployable Go router for Thompson Sampling.
// Build: go build -o router ./go/router
// Run (legacy): PORT=8080 EVIDENCE_PATH=./evidence.jsonl ARMS=a,b go run ./go/router
// Run (verified): ROUTER_MODE=verified STRATEGY_ID=... DECISIONS_PATH=... OUTCOMES_PATH=...
//
//	SETTLE_TOKEN=... SETTLE_ADDR=127.0.0.1:8081 go run ./go/router
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// appConfig is the full process configuration, loaded from the environment
// so it can be validated (and refused) before anything serves traffic.
type appConfig struct {
	port         string
	evidencePath string
	arms         []string
	providerURLs map[string]string

	shadowRate    float64
	shadowTimeout time.Duration
	shadowMaxConc int

	mode       gateway.RouterMode
	strategyID string
	mapper     string
	// selectionSeed, when set, seeds every request RNG identically
	// (deterministic selection for reproducible dry runs; production
	// omits it for time-seeded randomness).
	selectionSeed *uint64
	// checkpointEvery cadences verified-learning checkpoints; <=0 disables.
	checkpointEvery time.Duration

	decisionsPath string
	outcomesPath  string
	settleToken   string
	settleAddr    string
}

// loadConfig reads the environment. It never touches the network or disk:
// callers validate the result before opening stores or listeners.
func loadConfig(getenv func(string) string) (appConfig, error) {
	var c appConfig
	c.port = getenv("PORT")
	if c.port == "" {
		c.port = "8080"
	}
	c.evidencePath = getenv("EVIDENCE_PATH")
	if c.evidencePath == "" {
		c.evidencePath = "./evidence.jsonl"
	}
	armsEnv := getenv("ARMS")
	if armsEnv == "" {
		c.arms = []string{"openai/gpt-4", "anthropic/claude-3-opus"}
	} else {
		for _, a := range strings.Split(armsEnv, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				c.arms = append(c.arms, a)
			}
		}
	}
	if len(c.arms) == 0 {
		return c, fmt.Errorf("router: no arms configured")
	}
	c.providerURLs = make(map[string]string)
	for _, arm := range c.arms {
		key := "PROVIDER_URL_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(arm, "/", "_"), "-", "_"))
		if url := getenv(key); url != "" {
			c.providerURLs[arm] = url
		}
	}

	c.shadowRate = 0.0
	if v := getenv("SHADOW_SAMPLE_RATE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return c, fmt.Errorf("router: bad SHADOW_SAMPLE_RATE: %w", err)
		}
		if f < 0 || f > 1 {
			return c, fmt.Errorf("router: SHADOW_SAMPLE_RATE %v outside [0, 1]", f)
		}
		c.shadowRate = f
	}
	c.shadowTimeout = 5 * time.Second
	if v := getenv("SHADOW_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("router: bad SHADOW_TIMEOUT: %w", err)
		}
		if d < 0 {
			return c, fmt.Errorf("router: negative SHADOW_TIMEOUT %v", d)
		}
		c.shadowTimeout = d
	}
	c.shadowMaxConc = 5
	if v := getenv("SHADOW_MAX_CONCURRENCY"); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("router: bad SHADOW_MAX_CONCURRENCY: %w", err)
		}
		if i < 0 {
			return c, fmt.Errorf("router: negative SHADOW_MAX_CONCURRENCY %d", i)
		}
		c.shadowMaxConc = i
	}

	switch m := getenv("ROUTER_MODE"); m {
	case "", "legacy":
		c.mode = gateway.LegacyMode
	case "verified":
		c.mode = gateway.VerifiedMode
	default:
		return c, fmt.Errorf("router: unknown ROUTER_MODE %q", m)
	}
	c.strategyID = getenv("STRATEGY_ID")

	if c.mode == gateway.VerifiedMode {
		// Fail closed: every required verified input must be present before
		// any store opens or any listener binds.
		c.decisionsPath = getenv("DECISIONS_PATH")
		c.outcomesPath = getenv("OUTCOMES_PATH")
		c.settleToken = getenv("SETTLE_TOKEN")
		if c.decisionsPath == "" || c.outcomesPath == "" || c.settleToken == "" {
			return c, fmt.Errorf("router: verified mode requires DECISIONS_PATH, OUTCOMES_PATH and SETTLE_TOKEN")
		}
		c.settleAddr = getenv("SETTLE_ADDR")
		if c.settleAddr == "" {
			c.settleAddr = "127.0.0.1:8081"
		}
		// The settlement endpoint writes to the learning ledger: binding it
		// beyond loopback requires an explicit, documented operator override.
		// Authentication alone is not sufficient against network exposure.
		if !isLoopbackAddr(c.settleAddr) && getenv("ALLOW_PUBLIC_SETTLE") != "1" {
			return c, fmt.Errorf("router: SETTLE_ADDR %q is not loopback: set ALLOW_PUBLIC_SETTLE=1 to acknowledge public settlement exposure", c.settleAddr)
		}
		c.mapper = getenv("MAPPER")
		if v := getenv("SELECTION_SEED"); v != "" {
			sd, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return c, fmt.Errorf("router: bad SELECTION_SEED: %w", err)
			}
			c.selectionSeed = &sd
		}
		c.checkpointEvery = 60 * time.Second
		if v := getenv("CHECKPOINT_INTERVAL"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return c, fmt.Errorf("router: bad CHECKPOINT_INTERVAL: %w", err)
			}
			c.checkpointEvery = d
		}
	}
	return c, nil
}

// isLoopbackAddr reports whether addr resolves to a loopback interface
// (127.0.0.0/8, ::1, or localhost). Anything else is treated as public.
func isLoopbackAddr(addr string) bool {
	host := addr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			host = addr[:i]
			break
		}
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// bearerAuth returns a settlement auth hook comparing against a fixed token
// in constant time. Empty token never authenticates.
func bearerAuth(token string) func(r *http.Request) bool {
	return func(r *http.Request) bool {
		got := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(got, prefix) {
			return false
		}
		got, want := got[len(prefix):], token
		if len(got) == 0 || len(want) == 0 || len(got) != len(want) {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	}
}

// buildRouter opens stores, registers providers, and constructs the Router.
// Verified mode refuses to start unless every durable dependency opens.
func buildRouter(cfg appConfig) (*gateway.Router, func(), error) {
	policy := thompson.NewDefault(cfg.arms...)

	writer, err := gateway.NewFileEvidenceWriter(cfg.evidencePath)
	if err != nil {
		return nil, nil, fmt.Errorf("evidence writer: %w", err)
	}
	cleanup := func() { _ = writer.Close() }

	registry := gateway.NewProviderRegistry()
	for _, arm := range cfg.arms {
		if url, ok := cfg.providerURLs[arm]; ok {
			log.Printf("arm %s -> HTTP %s", arm, url)
			registry.Register(gateway.NewHTTPProvider(arm, gateway.HTTPProviderConfig{URL: url}))
		} else {
			log.Printf("arm %s -> FakeProvider (no real provider configured)", arm)
			registry.Register(gateway.NewFakeProvider(arm))
		}
	}

	rc := gateway.RouterConfig{
		Policy:               policy,
		Registry:             registry,
		Writer:               writer,
		StrategyID:           cfg.strategyID,
		Mode:                 cfg.mode,
		ShadowEligibility:    gateway.HeaderShadowEligibility{},
		ShadowSampleRate:     cfg.shadowRate,
		ShadowTimeout:        cfg.shadowTimeout,
		ShadowMaxConcurrency: cfg.shadowMaxConc,
	}
	if cfg.selectionSeed != nil {
		sd := *cfg.selectionSeed
		rc.RNGFactory = func() *rand.Rand { return rand.New(rand.NewPCG(sd, 0)) }
	}
	if cfg.mode == gateway.VerifiedMode {
		decisions, err := gateway.NewFileDecisionStore(cfg.decisionsPath)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("decision store: %w", err)
		}
		outcomes, err := outcome.NewFileOutcomeStore(cfg.outcomesPath)
		if err != nil {
			_ = decisions.Close()
			cleanup()
			return nil, nil, fmt.Errorf("outcome store: %w", err)
		}
		cleanup = func() {
			_ = outcomes.Close()
			_ = decisions.Close()
			_ = writer.Close()
		}
		rc.Decisions = decisions
		rc.Outcomes = outcomes
		rc.SettleAuth = bearerAuth(cfg.settleToken)
		switch cfg.mapper {
		case "", "binary":
		case "noop":
			rc.Mapper = outcome.NoopMapper{}
		default:
			cleanup()
			return nil, nil, fmt.Errorf("router: unknown MAPPER %q", cfg.mapper)
		}
	}
	router, err := gateway.NewRouter(rc)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("router: %w", err)
	}
	if cfg.mode == gateway.VerifiedMode {
		if err := router.RecoverVerifiedLearning(checkpointPath(cfg)); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("recovery: %w", err)
		}
	}
	return router, cleanup, nil
}

func checkpointPath(cfg appConfig) string {
	if cfg.outcomesPath == "" {
		return ""
	}
	return cfg.outcomesPath + ".checkpoint.json"
}

// publicMux serves inference traffic. Settlement is never mounted here:
// /v1/outcomes is explicitly refused so a misdirected settlement can never
// be mistaken for (or executed as) an inference request. Outcome writes
// arrive only on the internal listener.
func publicMux(router *gateway.Router) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", router.HealthHandler)
	mux.HandleFunc("/v1/outcomes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "settlement is not served on the public listener", http.StatusNotFound)
	})
	mux.Handle("/", router)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("# HELP router_requests_total placeholder\n"))
	})
	return mux
}

// internalMux serves the settlement API on a loopback-restricted listener.
func internalMux(router *gateway.Router) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", router.HealthHandler)
	mux.HandleFunc("/v1/outcomes", router.SettleHandler)
	return mux
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe /health and exit (for Docker HEALTHCHECK)")
	flag.Parse()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if *healthcheck {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://localhost:" + port + "/health")
		if err != nil {
			log.Printf("healthcheck failed: %v", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("healthcheck status: %d", resp.StatusCode)
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	router, cleanup, err := buildRouter(cfg)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer cleanup()

	srv := serve(cfg, router)
	waitForSignal()
	shutdown(srv, cfg, router, cleanup)
}

// servers holds both listeners so shutdown drains them together.
type servers struct {
	public   *http.Server
	internal *http.Server
}

// serve binds and serves in the background. Production passes fixed addrs;
// tests pass :0 addrs. Either way shutdown() drains both.
func serve(cfg appConfig, router *gateway.Router) *servers {
	srv := &servers{
		public: &http.Server{Addr: ":" + cfg.port, Handler: publicMux(router)},
	}
	go func() {
		log.Printf("router listening on %s mode=%s strategy=%s arms=%v", srv.public.Addr, cfg.mode, cfg.strategyID, cfg.arms)
		log.Printf("ownership: single Policy instance per process (sync.Mutex in Policy, single replica V0)")
		// Fatal on bind failure: a half-alive gateway (no listeners) would
		// pass health checks served by a stale process squatting the same
		// port and route traffic to the wrong files. ErrServerClosed is the
		// normal shutdown path.
		if err := srv.public.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("public listener: %v", err)
		}
	}()
	if cfg.mode == gateway.VerifiedMode {
		srv.internal = &http.Server{Addr: cfg.settleAddr, Handler: internalMux(router)}
		go func() {
			log.Printf("settlement listening on %s (internal only)", cfg.settleAddr)
			if err := srv.internal.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("internal listener: %v", err)
			}
		}()
		// Checkpoint cadence: bounded replay time at the cost of one sync
		// write per interval. SIGTERM/SIGINT checkpoints once more and exits
		// cleanly; SIGKILL falls back to ledger replay (slower, still exact).
		// Checkpoint calls serialize on the router's settleMu.
		if cfg.checkpointEvery > 0 {
			go func() {
				t := time.NewTicker(cfg.checkpointEvery)
				defer t.Stop()
				for range t.C {
					if err := router.CheckpointVerifiedLearning(checkpointPath(cfg)); err != nil {
						log.Printf("checkpoint: %v", err)
					}
				}
			}()
		}
	}
	return srv
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}

// shutdown drains in-flight requests against a deadline, writes one final
// checkpoint in verified mode, then releases stores. Ticker and shutdown
// checkpoints serialize on settleMu; a torn checkpoint tmp file is
// overwritten by the next save, and a torn final checkpoint fails the next
// boot loudly (delete it to force a full ledger rebuild).
func shutdown(srv *servers, cfg appConfig, router *gateway.Router, cleanup func()) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if srv.internal != nil {
		_ = srv.internal.Shutdown(ctx)
	}
	_ = srv.public.Shutdown(ctx)
	if cfg.mode == gateway.VerifiedMode {
		if err := router.CheckpointVerifiedLearning(checkpointPath(cfg)); err != nil {
			log.Printf("shutdown checkpoint: %v", err)
		}
	}
	cleanup()
}
