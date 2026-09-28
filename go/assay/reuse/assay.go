package reuse

import (
	"fmt"
	"time"
)

// assay.go: three-mode comparison driver (Phase 4).
// B0 no-reuse (always executes; doubles as fresh-execution ground truth),
// B1 naive exact cache keyed ONLY by (operation, primary input digest),
// B2 verified dependency-aware reuse (full ExecutionKey + evidence).
// Every reuse decision is auditable via DecisionLog.

// Mode IDs.
const (
	ModeB0 = "B0"
	ModeB1 = "B1"
	ModeB2 = "B2"
)

// NaiveKey is the explicitly documented simpler baseline key: operation +
// primary input digest ONLY. It deliberately omits toolchain, config,
// verifier contract, executor version and environment.
func NaiveKey(w World) string {
	if w.Workload == WorkloadW1 {
		return "w1.validate|" + w.Tree.TreeDigest()
	}
	return "w2.extract|" + w.Doc.DocDigest()
}

// ModeMetrics accumulates the primary metrics (Phase 4) for one mode.
type ModeMetrics struct {
	Mode                string   `json:"mode"`
	Workload            string   `json:"workload"`
	Runs                int      `json:"runs"`
	Hits                int      `json:"hits"`
	CorrectReuse        int      `json:"correct_reuse"`
	FalseReuse          int      `json:"false_reuse"`
	IncorrectInvalidate int      `json:"incorrect_invalidation"`
	FreshExecutions     int      `json:"fresh_executions"`
	FreshAvoided        int      `json:"fresh_avoided"`
	CostAvoidedUSD      float64  `json:"cost_avoided_usd"`
	CostSpentUSD        float64  `json:"cost_spent_usd"`
	VerifyOverheadNS    int64    `json:"verify_overhead_ns"`
	WallNS              int64    `json:"wall_ns"`
	StorageBytes        int64    `json:"storage_bytes"`
	Artifacts           int      `json:"artifacts"`
	FalseReuseCases     []string `json:"false_reuse_cases"`
	IncorrectInvCases   []string `json:"incorrect_invalidation_cases"`
}

// Scenario is one frozen evaluation case: seed x mutation.
type Scenario struct {
	Workload string
	Seed     uint64
	Mutation Mutation
}

// Matrix builds the frozen evaluation matrix over workloads x seeds x mutations.
func Matrix(workloads []string, seeds []uint64) []Scenario {
	var out []Scenario
	for _, wl := range workloads {
		for _, s := range seeds {
			for _, m := range Mutations(wl) {
				out = append(out, Scenario{Workload: wl, Seed: s, Mutation: m})
			}
		}
	}
	return out
}

// Runner executes one mode over the matrix. B1/B2 keep cache state across
// scenarios with the same (workload, seed) base world, modeling a warm cache:
// each scenario first warms the cache with the base world, then probes the
// mutated world.
type Runner struct {
	Store         *Store
	Naive         map[string][]byte  // B1 cache: naive key -> bytes
	NaiveMeta     map[string]string  // B1 cache: naive key -> bound verifier label (for audit only; B1 does NOT check it)
	Outcomes      map[string]Outcome // authoritative outcome view
	Bodies        map[string][]byte  // artifact bytes by artifact digest (content store)
	BoundVerifier map[string]string  // key digest -> verifier label bound at publish
	JobSeq        int
	lastJob       string
}

func NewRunner(s *Store) *Runner {
	return &Runner{Store: s, Naive: map[string][]byte{}, NaiveMeta: map[string]string{}, Outcomes: map[string]Outcome{}, Bodies: map[string][]byte{}, BoundVerifier: map[string]string{}}
}

func (r *Runner) nextJob() string {
	r.JobSeq++
	return fmt.Sprintf("job-%d", r.JobSeq)
}

// outcomeLookup resolves against the runner's authoritative view.
func (r *Runner) outcomeLookup(jobID string) Outcome {
	if o, ok := r.Outcomes[jobID]; ok {
		return o
	}
	return Outcome{}
}

// RunMode executes mode over scenarios, returning metrics. publishBase controls
// whether fresh base executions are published to the store (B2 path).
func (r *Runner) RunMode(mode string, scenarios []Scenario) *ModeMetrics {
	m := &ModeMetrics{Mode: mode}
	start := time.Now()
	// Group by (workload, seed): warm base, then probe each mutation.
	type group struct {
		wl   string
		seed uint64
		sc   []Scenario
	}
	groups := map[string]*group{}
	order := []string{}
	for _, sc := range scenarios {
		k := sc.Workload + "/" + fmt.Sprint(sc.Seed)
		g, ok := groups[k]
		if !ok {
			g = &group{wl: sc.Workload, seed: sc.Seed}
			groups[k] = g
			order = append(order, k)
		}
		g.sc = append(g.sc, sc)
		if m.Workload == "" {
			m.Workload = sc.Workload
		}
	}
	for _, k := range order {
		g := groups[k]
		base := BaseWorld(g.wl, g.seed)
		baseBody, baseCost := Execute(base)
		job := r.nextJob()
		r.lastJob = job
		r.Outcomes[job] = Outcome{JobID: job, Version: 1, Status: "ACCEPTED", Found: true}
		if mode == ModeB2 {
			r.Store.SetOutcome(r.Outcomes[job])
			if _, err := r.Store.Publish(KeyFor(base), PublishBody{
				ArtifactDigest: DigestBytes(baseBody),
				Verification:   VerificationAccepted,
				EvidenceID:     "ev-" + job,
				VerifiedAt:     "2026-09-28T00:00:00Z",
				ActualCostUSD:  baseCost,
				ReceiptID:      "rcpt-" + job,
				OutcomeJobID:   job,
				OutcomeVersion: 1,
			}); err != nil {
				panic(err)
			}
			r.Bodies[DigestBytes(baseBody)] = baseBody
			r.BoundVerifier[KeyFor(base).KeyDigest()] = base.Verifier
		}
		if mode == ModeB1 {
			r.Naive[NaiveKey(base)] = baseBody
			r.NaiveMeta[NaiveKey(base)] = base.Verifier
		}
		for _, sc := range g.sc {
			r.runScenario(mode, m, base, baseBody, baseCost, job, sc)
		}
	}
	m.WallNS = time.Since(start).Nanoseconds()
	if a, _, by := r.Store.GraphStats(); mode == ModeB2 {
		m.Artifacts, m.StorageBytes = a, by
	}
	return m
}

func (r *Runner) runScenario(mode string, m *ModeMetrics, base World, baseBody []byte, baseCost float64, baseJob string, sc Scenario) {
	m.Runs++
	w := BaseWorld(sc.Workload, sc.Seed)
	sc.Mutation.Mutate(&w)
	// Fresh-execution ground truth (B0 oracle): always recompute.
	freshBody, freshCost := Execute(w)
	freshOK := Verify(w, freshBody)
	if !freshOK {
		panic("fresh execution failed its own verifier")
	}
	// Authoritative correction: revokes the base outcome BEFORE the probe.
	job := baseJob
	if w.CorrectRevoked {
		r.Outcomes[job] = Outcome{JobID: job, Version: 2, Status: "REJECTED", Found: true}
		r.Store.SetOutcome(r.Outcomes[job])
	}
	r.lastJob = job
	switch mode {
	case ModeB0:
		m.FreshExecutions++
		m.CostSpentUSD += freshCost
	case ModeB1:
		r.runB1(m, w, freshBody, freshCost, sc)
	case ModeB2:
		r.runB2(m, w, base, freshBody, freshCost, job, sc)
	}
	_ = baseBody
	_ = baseCost
}

// isFalseReuse: reused bytes are a correctness failure if they differ from
// fresh ground truth, fail the current independent verifier, are bound to a
// stale verifier contract, or lack an ACCEPTED authoritative outcome.
func isFalseReuse(w World, reused, fresh []byte, boundVerifier string, outcome Outcome) (bool, string) {
	if !equalBytes(reused, fresh) {
		return true, "bytes differ from fresh execution"
	}
	if !Verify(w, reused) {
		return true, "reused bytes fail current independent verifier"
	}
	if boundVerifier != w.Verifier {
		return true, "bound verifier contract != current verifier contract"
	}
	if !outcome.Found || outcome.Status != "ACCEPTED" {
		return true, "no ACCEPTED authoritative outcome"
	}
	return false, ""
}

func (r *Runner) runB1(m *ModeMetrics, w World, freshBody []byte, freshCost float64, sc Scenario) {
	nk := NaiveKey(w)
	reused, hit := r.Naive[nk]
	if !hit {
		m.FreshExecutions++
		m.CostSpentUSD += freshCost
		r.Naive[nk] = freshBody
		r.NaiveMeta[nk] = w.Verifier
		// Missed a reuse opportunity only if bytes+contract+outcome all match base.
		return
	}
	m.Hits++
	t0 := time.Now()
	oc := r.currentOutcome(w)
	m.VerifyOverheadNS += time.Since(t0).Nanoseconds()
	// B1 binds no outcome: it reuses purely on input equality. For the audit,
	// resolve the authoritative outcome for this (workload,seed) group via the
	// scenario's base job recorded in NaiveMeta? B1 has no job link — model the
	// real naive-cache behavior: no outcome check at all. The assay grades the
	// reused bytes against ground truth + current verifier + current outcome.
	bound := r.NaiveMeta[nk]
	if bad, reason := isFalseReuse(w, reused, freshBody, bound, oc); bad {
		m.FalseReuse++
		m.FalseReuseCases = append(m.FalseReuseCases,
			fmt.Sprintf("%s/seed%d/%s: %s", sc.Workload, sc.Seed, sc.Mutation.Name, reason))
	} else {
		m.CorrectReuse++
		m.FreshAvoided++
		m.CostAvoidedUSD += freshCost
	}
}

// groupJob tracks the current (workload,seed) base job for grading B1.
func (r *Runner) currentOutcome(w World) Outcome {
	// Outcomes map is keyed by job; find the latest job for this seed group.
	// The runner processes one group at a time, so track last job per group.
	return r.Outcomes[r.lastJob]
}

func (r *Runner) runB2(m *ModeMetrics, w World, base World, freshBody []byte, freshCost float64, job string, sc Scenario) {
	r.lastJob = job
	cur := KeyFor(w)
	t0 := time.Now()
	stored, hit := r.Store.Lookup(cur.KeyDigest())
	var dec Decision
	if hit {
		live := liveOf(cur)
		dec = r.Store.Evaluate(cur, live, r.outcomeLookup)
	}
	m.VerifyOverheadNS += time.Since(t0).Nanoseconds()
	if !hit {
		// Cache miss: validity expectation check — a miss is correct unless
		// the mutation was supposed to preserve validity AND the key is
		// identical (i.e. we lost a hit we should have had).
		m.FreshExecutions++
		m.CostSpentUSD += freshCost
		if sc.Mutation.ExpectB2 == ValidityValid && cur.KeyDigest() == KeyFor(base).KeyDigest() {
			m.IncorrectInvalidate++
			m.IncorrectInvCases = append(m.IncorrectInvCases,
				fmt.Sprintf("%s/seed%d/%s: valid key missed", sc.Workload, sc.Seed, sc.Mutation.Name))
		}
		// Publish the fresh ACCEPTED execution. Skipped only when the
		// authoritative outcome is revoked (verification cannot pass, so
		// nothing is publishable). Verifier-changed worlds ARE published:
		// the fresh result is verified under the NEW contract and binds a
		// distinct key; the old-contract artifact is untouched.
		if !w.CorrectRevoked {
			if _, err := r.Store.Publish(cur, PublishBody{
				ArtifactDigest: DigestBytes(freshBody),
				Verification:   VerificationAccepted,
				EvidenceID:     "ev-" + job + "-m",
				VerifiedAt:     "2026-09-28T00:00:00Z",
				ActualCostUSD:  freshCost,
				ReceiptID:      "rcpt-" + job + "-m",
				OutcomeJobID:   job,
				OutcomeVersion: r.Outcomes[job].Version,
			}); err != nil {
				panic(err)
			}
			r.Bodies[DigestBytes(freshBody)] = freshBody
			r.BoundVerifier[cur.KeyDigest()] = w.Verifier
		}
		return
	}
	m.Hits++
	if dec.Validity != ValidityValid {
		// Correct non-reuse if the mutation demanded it.
		m.FreshExecutions++
		m.CostSpentUSD += freshCost
		if sc.Mutation.ExpectB2 == ValidityValid {
			m.IncorrectInvalidate++
			m.IncorrectInvCases = append(m.IncorrectInvCases,
				fmt.Sprintf("%s/seed%d/%s: valid reuse refused (%s)", sc.Workload, sc.Seed, sc.Mutation.Name, dec.Reason))
		}
		return
	}
	// Reuse path: cryptographic binding check, then grade against ground
	// truth (zero-false-reuse gate).
	reused := mustBody(r, stored)
	if err := r.Store.VerifyBytes(cur.KeyDigest(), reused); err != nil {
		m.FalseReuse++
		m.FalseReuseCases = append(m.FalseReuseCases,
			fmt.Sprintf("%s/seed%d/%s: B2 binding failure: %v", sc.Workload, sc.Seed, sc.Mutation.Name, err))
		return
	}
	oc := r.outcomeLookup(stored.OutcomeJobID)
	if bad, reason := isFalseReuse(w, reused, freshBody, r.BoundVerifier[stored.KeyDigest], oc); bad {
		m.FalseReuse++
		m.FalseReuseCases = append(m.FalseReuseCases,
			fmt.Sprintf("%s/seed%d/%s: B2 %s", sc.Workload, sc.Seed, sc.Mutation.Name, reason))
		return
	}
	m.CorrectReuse++
	m.FreshAvoided++
	m.CostAvoidedUSD += freshCost
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mustBody resolves stored artifact bytes from the content store.
func mustBody(r *Runner, stored *Artifact) []byte {
	b, ok := r.Bodies[stored.ArtifactDigest]
	if !ok {
		panic("reuse: body missing for artifact " + stored.ArtifactDigest[:16])
	}
	return b
}
