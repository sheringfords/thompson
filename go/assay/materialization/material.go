package materialization

import (
	"fmt"
	"sort"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// material.go: mode C (verified dependency-aware materialization) and mode D
// (C plus deterministic staged/direct report selection). C resolves every
// computation against the reuse store with explicit dependency edges; only
// the invalidation closure executes. D adds a calibrated quote between two
// predeclared report paths. No workload-specific invalidation code: all
// dependency logic is generic key/validity evaluation.

// matNode resolves one computation: reuse iff VALID, else execute + verify
// + publish. Returns bytes and whether they were reused (reused terminals
// skip semantic re-verification by evidence).
func (r *Runner) matNode(res *VersionResult, op, opVer, input string,
	deps []reuse.Dep, compute func() []byte) ([]byte, bool) {
	t0 := time.Now()
	key := r.cKey(op, opVer, input, deps, r.Contracts[op])
	overhead(res, time.Since(t0))
	t1 := time.Now()
	stored, hit := r.Store.Lookup(key.KeyDigest())
	overhead(res, time.Since(t1))
	if hit {
		t2 := time.Now()
		dec := r.Store.Evaluate(key, reuse.LiveOf(key), r.lookup)
		overhead(res, time.Since(t2))
		switch dec.Validity {
		case reuse.ValidityValid:
			b, ok := r.Bodies[stored.ArtifactDigest]
			if !ok {
				res.Verdict = "REJECTED"
				return nil, false
			}
			t3 := time.Now()
			err := r.Store.VerifyBytes(key.KeyDigest(), b)
			overhead(res, time.Since(t3))
			if err != nil {
				res.Verdict = "REJECTED"
				return nil, false
			}
			res.Reused++
			var cc string
			for _, d := range stored.Deps {
				if d.Name == "verifier_contract" {
					cc = d.Digest
				}
			}
			r.LastAudits = append(r.LastAudits, ReuseAudit{Op: op, Key: key.KeyDigest(),
				Contract: cc, Job: stored.OutcomeJobID, Version: stored.OutcomeVersion,
				Bytes: stored.ArtifactDigest})
			return b, true
		case reuse.ValidityStale:
			res.StaleN++
		case reuse.ValidityInvalid:
			res.InvalidN++
		}
	}
	out := execOp(res, compute)
	job := r.nextJob(op)
	if op == "verify" && !r.verifyJobSeen[job] {
		r.verifyJobSeen[job] = true
		r.verifyJobs = append(r.verifyJobs, job)
		r.AllVerifyJobs = append(r.AllVerifyJobs, job)
	}
	r.Outcomes[job] = reuse.Outcome{JobID: job, Version: 1, Status: "ACCEPTED", Found: true}
	r.Store.SetOutcome(r.Outcomes[job])
	pub := func() (*reuse.Artifact, error) {
		return r.Store.Publish(key, reuse.PublishBody{
			ArtifactDigest: digestOf(out), Verification: reuse.VerificationAccepted,
			EvidenceID: "ev-" + job, VerifiedAt: "2026-09-29T00:00:00Z",
			ActualCostUSD: 0, ReceiptID: "rcpt-" + job,
			OutcomeJobID: job, OutcomeVersion: 1,
		})
	}
	t4 := time.Now()
	art, err := pub()
	if err != nil {
		// Republication over non-VALID records (post-correction recompute).
		art, err = r.Store.Republish(key, reuse.PublishBody{
			ArtifactDigest: digestOf(out), Verification: reuse.VerificationAccepted,
			EvidenceID: "ev-" + job, VerifiedAt: "2026-09-29T00:00:00Z",
			ActualCostUSD: 0, ReceiptID: "rcpt-" + job,
			OutcomeJobID: job, OutcomeVersion: 1,
		})
	}
	overhead(res, time.Since(t4))
	if err != nil {
		res.Verdict = "REJECTED"
		return nil, false
	}
	r.Bodies[art.ArtifactDigest] = out
	return out, false
}

// runCD executes the materialized pipeline; direct selects the report path.
func (r *Runner) runCD(c Corpus, res *VersionResult, selectPlan bool) {
	sch, tax, agg := schemaD(c), taxD(c), aggD(c)
	enriched := map[string][]byte{}
	verifyM := map[string][]byte{}
	for _, rec := range c.Records {
		rd := RecordDigest(rec)
		v, _ := r.matNode(res, "validate", "v1", rd, nil, func() []byte { return ValidateOp(rec) })
		_ = v
		n, _ := r.matNode(res, "normalize", "v1", rd, []reuse.Dep{dep2("schema", sch)},
			func() []byte { return NormalizeOp(rec, c.Schema) })
		e, _ := r.matNode(res, "extract", "v1", digestOf(n), []reuse.Dep{dep2("schema", sch)},
			func() []byte { return ExtractOp(n, c.Schema) })
		vv, _ := r.matNode(res, "verify", "v1", rd, []reuse.Dep{{Name: "upstream:ext", Digest: digestOf(e)}},
			func() []byte { return VerifyOp(rec, e) })
		en, _ := r.matNode(res, "enrich", "v1", digestOf(e), []reuse.Dep{dep2("taxonomy", tax)},
			func() []byte { return EnrichOp(e, c.Taxonomy) })
		verifyM[rec.ID] = vv
		enriched[rec.ID] = en
		if res.Verdict == "REJECTED" {
			return
		}
	}
	// Sharded aggregation (staged path) or plan selection (D quotes BEFORE
	// suffix resolution so unexecuted shards price as execute).
	roll := VerifyRollup(verifyM)
	if selectPlan {
		r.runDReport(c, res, enriched, verifyM, roll, agg)
		return
	}
	shardB := r.execShards(c, res, enriched, agg)
	var cups []reuse.Dep
	for _, sid := range sortedShardIDs(shardB) {
		cups = append(cups, reuse.Dep{Name: reuse.UpstreamPrefix + sid, Digest: digestOf(shardB[sid])})
	}
	comb, _ := r.matNode(res, "combine", "v1", digestOf([]byte("combine")), cups,
		func() []byte { return AggregateOp(shardB, c.AggParam) })
	rep, _ := r.matNode(res, "report", "v1", digestOf(comb),
		[]reuse.Dep{{Name: "upstream:combine", Digest: digestOf(comb)}, dep2("aggparam", agg)},
		func() []byte { return ReportOp(comb) })
	r.attestNode(res, rep, roll)
}

// attestNode resolves the terminal verification (evidence-bound in C/D:
// VALID-reused terminals skip semantic re-verification; recomputed terminals
// verify fresh and set the version verdict).
func (r *Runner) attestNode(res *VersionResult, report []byte, roll string) {
	if report == nil {
		res.Verdict = "REJECTED"
		return
	}
	out, reused := r.matNode(res, "attest", "v1", digestOf(report),
		[]reuse.Dep{{Name: "upstream:report", Digest: digestOf(report)}, {Name: "verify-rollup", Digest: reuse.DigestString(roll)}},
		func() []byte { return AttestOp(report, roll) })
	if out == nil {
		if res.Verdict == "" {
			res.Verdict = "REJECTED"
		}
		return
	}
	if reused {
		res.Terminal = digestOf(out)
		return // evidence covers re-verification
	}
	t0 := time.Now()
	again := AttestOp(report, roll)
	res.VerifyNS += time.Since(t0).Nanoseconds()
	if !verifyOpOut(again, out) {
		res.Verdict = "REJECTED"
		return
	}
	res.Terminal = digestOf(out)
}

// runDReport quotes staged-remaining vs direct-remaining and executes the winner.
// NOTE: called BEFORE suffix resolution (shards unresolved): quotes price
// reconstructed keys, so cold/changed shards count as execute. Quoting after
// execution would see only VALID records and always pick staged.
func (r *Runner) runDReport(c Corpus, res *VersionResult, enriched, verifyM map[string][]byte, roll, agg string) {
	stagedPrice := r.priceStagedPre(enriched, agg, roll)
	directPrice := r.priceDirect(enriched, agg, roll)
	var shardB map[string][]byte
	var rep []byte
	if directPrice < stagedPrice {
		// Tie breaks to staged (deterministic; reported as zero-value ties).
		res.Plan = "direct"
		rep, _ = r.matNode(res, "direct", "v1", digestOf([]byte(roll+agg)),
			append([]reuse.Dep{dep2("aggparam", agg)}, enrichEdges(enriched)...),
			func() []byte { return DirectOp(enriched, c.AggParam) })
	} else {
		res.Plan = "staged"
		shardB = r.execShards(c, res, enriched, agg)
		var cups []reuse.Dep
		for _, sid := range sortedShardIDs(shardB) {
			cups = append(cups, reuse.Dep{Name: reuse.UpstreamPrefix + sid, Digest: digestOf(shardB[sid])})
		}
		comb, _ := r.matNode(res, "combine", "v1", digestOf([]byte("combine")), cups,
			func() []byte { return AggregateOp(shardB, c.AggParam) })
		rep, _ = r.matNode(res, "report", "v1", digestOf(comb),
			[]reuse.Dep{{Name: "upstream:combine", Digest: digestOf(comb)}, dep2("aggparam", agg)},
			func() []byte { return ReportOp(comb) })
	}
	r.attestNode(res, rep, roll)
}

// execShards resolves the staged shard layer.
func (r *Runner) execShards(c Corpus, res *VersionResult, enriched map[string][]byte, agg string) map[string][]byte {
	byShard := map[int]map[string][]byte{}
	for id, b := range enriched {
		s := shardOf(id, Shards)
		if byShard[s] == nil {
			byShard[s] = map[string][]byte{}
		}
		byShard[s][id] = b
	}
	shardB := map[string][]byte{}
	for s := 0; s < Shards; s++ {
		m := byShard[s]
		if m == nil {
			m = map[string][]byte{}
		}
		sid := fmt.Sprintf("shard-%02d", s)
		var ups []reuse.Dep
		for _, id := range sortedIDs(m) {
			ups = append(ups, reuse.Dep{Name: reuse.UpstreamPrefix + id, Digest: digestOf(m[id])})
		}
		mm := m
		sb, _ := r.matNode(res, "shardagg", "v1", digestOf([]byte(sid)),
			append([]reuse.Dep{dep2("aggparam", agg)}, ups...),
			func() []byte { return AggregateOp(mm, c.AggParam) })
		shardB[sid] = sb
	}
	return shardB
}

// Quoter weights are DECLARED work units per op (deterministic, predeclared
// in WORKLOAD.md): selection must be reproducible, so quotes do not use noisy
// ns calibration. Measured ns remain the work-accounting basis.
var quoteWeights = map[string]float64{
	"shardagg": 8, "combine": 8, "report": 2, "direct": 30, "attest": 4,
}

const quoteVerifyUnit = 1.0

// priceStaged estimates remaining staged cost in declared units: 0 per
// VALID node, weight+verify otherwise. Conservative where uncomputable.
// priceStagedPre estimates remaining staged cost in declared units BEFORE
// suffix resolution: shard keys reconstructed from current enriched bytes
// (0 when VALID, weight+verify otherwise); combine/report/attest priced via
// store artifacts where the full shard set is VALID, else as execute.
func (r *Runner) priceStagedPre(enriched map[string][]byte, agg, roll string) float64 {
	return r.priceStaged(enriched, agg, roll)
}

func (r *Runner) priceStaged(enriched map[string][]byte, agg, roll string) float64 {
	total := 0.0
	byShard := map[int]map[string][]byte{}
	for id, b := range enriched {
		s := shardOf(id, Shards)
		if byShard[s] == nil {
			byShard[s] = map[string][]byte{}
		}
		byShard[s][id] = b
	}
	combArts := map[string]string{} // sid -> current shard artifact digest
	combKeys := map[string]string{}
	for s := 0; s < Shards; s++ {
		sid := fmt.Sprintf("shard-%02d", s)
		var ups []reuse.Dep
		for _, id := range sortedIDs(byShard[shardIndex(sid)]) {
			ups = append(ups, reuse.Dep{Name: reuse.UpstreamPrefix + id, Digest: digestOf(byShard[shardIndex(sid)][id])})
		}
		k := r.cKey("shardagg", "v1", digestOf([]byte(sid)),
			append([]reuse.Dep{{Name: "aggparam", Digest: agg}}, ups...), r.Contracts["shardagg"])
		combKeys[sid] = k.KeyDigest()
		if stored, hit := r.Store.Lookup(k.KeyDigest()); hit {
			if dec := r.Store.Evaluate(k, reuse.LiveOf(k), r.lookup); dec.Validity == reuse.ValidityValid {
				combArts[sid] = stored.ArtifactDigest
				continue
			}
		}
		total += quoteWeights["shardagg"] + quoteVerifyUnit
	}
	var cups []reuse.Dep
	combKnown := true
	for s := 0; s < Shards; s++ {
		sid := fmt.Sprintf("shard-%02d", s)
		ad, ok := combArts[sid]
		if !ok {
			combKnown = false
			break
		}
		cups = append(cups, reuse.Dep{Name: reuse.UpstreamPrefix + combKeys[sid], Digest: ad})
	}
	combArt := ""
	if combKnown {
		ck := r.cKey("combine", "v1", digestOf([]byte("combine")), cups, r.Contracts["combine"])
		if stored, hit := r.Store.Lookup(ck.KeyDigest()); hit {
			if dec := r.Store.Evaluate(ck, reuse.LiveOf(ck), r.lookup); dec.Validity == reuse.ValidityValid {
				combArt = stored.ArtifactDigest
			} else {
				total += quoteWeights["combine"] + quoteVerifyUnit
			}
		} else {
			total += quoteWeights["combine"] + quoteVerifyUnit
		}
	} else {
		total += r.CalibNS["combine"] + 1000
	}
	if combArt != "" {
		rk := r.cKey("report", "v1", combArt,
			[]reuse.Dep{{Name: "upstream:combine", Digest: combArt}, dep2("aggparam", agg)}, r.Contracts["report"])
		if stored, hit := r.Store.Lookup(rk.KeyDigest()); hit {
			if dec := r.Store.Evaluate(rk, reuse.LiveOf(rk), r.lookup); dec.Validity == reuse.ValidityValid {
				ak := r.cKey("attest", "v1", stored.ArtifactDigest,
					[]reuse.Dep{{Name: "upstream:report", Digest: stored.ArtifactDigest},
						{Name: "verify-rollup", Digest: reuse.DigestString(roll)}}, r.Contracts["attest"])
				if r.validNow(ak) {
					return total
				}
			}
		}
		total += quoteWeights["report"] + quoteWeights["attest"] + 2*quoteVerifyUnit
	} else {
		total += quoteWeights["report"] + quoteWeights["attest"] + 2*quoteVerifyUnit
	}
	return total
}

// priceDirect estimates remaining direct cost in declared units.
func (r *Runner) priceDirect(enriched map[string][]byte, agg, roll string) float64 {
	k := r.cKey("direct", "v1", digestOf([]byte(roll+agg)),
		append([]reuse.Dep{{Name: "aggparam", Digest: agg}}, enrichEdges(enriched)...), r.Contracts["direct"])
	if r.validNow(k) {
		return quoteWeights["attest"] + quoteVerifyUnit
	}
	return quoteWeights["direct"] + quoteWeights["attest"] + 2*quoteVerifyUnit
}

func (r *Runner) validNow(k reuse.ExecutionKey) bool {
	_, hit := r.Store.Lookup(k.KeyDigest())
	if !hit {
		return false
	}
	return r.Store.Evaluate(k, reuse.LiveOf(k), r.lookup).Validity == reuse.ValidityValid
}

func enrichEdges(enriched map[string][]byte) []reuse.Dep {
	var out []reuse.Dep
	for _, id := range sortedIDs(enriched) {
		out = append(out, reuse.Dep{Name: reuse.UpstreamPrefix + id, Digest: digestOf(enriched[id])})
	}
	return out
}

func dep2(name, d string) reuse.Dep { return reuse.Dep{Name: name, Digest: d} }

func shardIndex(sid string) int {
	var n int
	fmt.Sscanf(sid, "shard-%02d", &n)
	return n
}

func sortedShardIDs(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
