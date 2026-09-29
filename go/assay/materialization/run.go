package materialization

import (
	"fmt"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// run.go: per-version pipeline execution for A/B/B+/C/D.

// Shards partitions record IDs (fixed, predeclared).
const Shards = 16

func shardOf(id string, n int) int {
	h := 0
	for i := 0; i < len(id); i++ {
		h = h*31 + int(id[i])
	}
	if h < 0 {
		h = -h
	}
	return h % n
}

// NewRunner builds a mode runner with calibration + default v1 contracts.
func NewRunner(mode ModeID, cache *FileCache, store *reuse.Store) *Runner {
	r := &Runner{Mode: mode, Cache: cache, Store: store,
		Outcomes: map[string]reuse.Outcome{}, Bodies: map[string][]byte{},
		Contracts: map[string]string{}, CalibNS: map[string]float64{}}
	for _, op := range []string{"validate", "normalize", "extract", "verify",
		"enrich", "shardagg", "combine", "report", "direct", "attest"} {
		r.Contracts[op] = ContractFor(op, "v1")
	}
	r.calibrate()
	return r
}

func (r *Runner) calibrate() {
	sample := []byte("calibration-bytes-0123456789")
	rec := Record{ID: "r000000", Name: "n", Amount: 1, Category: "a", Text: "t"}
	timed := func(fn func()) float64 {
		var total int64
		for i := 0; i < 5; i++ {
			t0 := time.Now()
			fn()
			total += time.Since(t0).Nanoseconds()
		}
		return float64(total) / 5
	}
	norm := NormalizeOp(rec, "schema/v3")
	ext := ExtractOp(norm, "schema/v3")
	r.CalibNS["validate"] = timed(func() { ValidateOp(rec) })
	r.CalibNS["normalize"] = timed(func() { NormalizeOp(rec, "schema/v3") })
	r.CalibNS["extract"] = timed(func() { ExtractOp(norm, "schema/v3") })
	r.CalibNS["verify"] = timed(func() { VerifyOp(rec, ext) })
	r.CalibNS["enrich"] = timed(func() { EnrichOp(ext, "tax/v1") })
	r.CalibNS["shardagg"] = timed(func() { AggregateOp(map[string][]byte{"x": ext}, "topk/10") })
	r.CalibNS["combine"] = timed(func() { AggregateOp(map[string][]byte{"x": ext}, "topk/10") })
	r.CalibNS["report"] = timed(func() { ReportOp(ext) })
	r.CalibNS["direct"] = timed(func() { DirectOp(map[string][]byte{"x": ext}, "topk/10") })
	r.CalibNS["attest"] = timed(func() { AttestOp(ext, "roll") })
	_ = sample
}

// schemaD digests the corpus schema label; taxD the taxonomy label.
func schemaD(c Corpus) string { return reuse.DigestString(c.Schema) }
func taxD(c Corpus) string    { return TaxonomyDigest(c.Taxonomy) }
func aggD(c Corpus) string    { return reuse.DigestString(c.AggParam) }

// RunVersion executes one corpus version under the runner's mode.
func (r *Runner) RunVersion(c Corpus, mutation string) *VersionResult {
	res := &VersionResult{Mode: string(r.Mode), Mutation: mutation, Records: len(c.Records)}
	r.LastAudits = nil
	r.verifyJobs = nil
	r.verifyJobSeen = map[string]bool{}
	switch r.Mode {
	case ModeA:
		r.runA(c, res)
	case ModeB:
		r.runBC(c, res, false)
	case ModeBp:
		r.runBC(c, res, true)
	case ModeC:
		r.runCD(c, res, false)
	case ModeD:
		r.runCD(c, res, true)
	}
	if res.Verdict == "" {
		res.Verdict = "ACCEPTED"
	}
	if r.Mode == ModeC || r.Mode == ModeD {
		_, _, by := r.storeStats()
		res.StoreBytes = by
	}
	return res
}

func (r *Runner) storeStats() (int, int, int64) {
	if r.Store == nil {
		return 0, 0, 0
	}
	a, e, by := r.Store.GraphStats()
	return a, e, by
}

// --- Mode A: full recomputation oracle ---

func (r *Runner) runA(c Corpus, res *VersionResult) {
	w := r.executeAll(c, res, true)
	res.Terminal = digestOf(w.attest)
}

type versionWork struct {
	enriched map[string][]byte
	verify   map[string][]byte
	agg      map[string][]byte // shardID -> bytes
	combine  []byte
	report   []byte
	attest   []byte
}

// executeAll runs every op fresh (used by A and by C/D misses).
func (r *Runner) executeAll(c Corpus, res *VersionResult, verifyOut bool) versionWork {
	w := versionWork{enriched: map[string][]byte{}, verify: map[string][]byte{}, agg: map[string][]byte{}}
	for _, rec := range c.Records {
		v := execOp(res, func() []byte { return ValidateOp(rec) })
		if verifyOut && string(v) == "INVALID" {
			res.Verdict = "REJECTED"
		}
		n := execOp(res, func() []byte { return NormalizeOp(rec, c.Schema) })
		e := execOp(res, func() []byte { return ExtractOp(n, c.Schema) })
		vv := execOp(res, func() []byte { return VerifyOp(rec, e) })
		en := execOp(res, func() []byte { return EnrichOp(e, c.Taxonomy) })
		w.verify[rec.ID] = vv
		w.enriched[rec.ID] = en
	}
	byShard := map[int]map[string][]byte{}
	for id, b := range w.enriched {
		s := shardOf(id, Shards)
		if byShard[s] == nil {
			byShard[s] = map[string][]byte{}
		}
		byShard[s][id] = b
	}
	for s := 0; s < Shards; s++ {
		m := byShard[s]
		if m == nil {
			m = map[string][]byte{}
		}
		sid := fmt.Sprintf("shard-%02d", s)
		w.agg[sid] = execOp(res, func() []byte { return AggregateOp(m, c.AggParam) })
	}
	w.combine = execOp(res, func() []byte { return AggregateOp(w.agg, c.AggParam) })
	w.report = execOp(res, func() []byte { return ReportOp(w.combine) })
	roll := VerifyRollup(w.verify)
	w.attest = execOp(res, func() []byte { return AttestOp(w.report, roll) })
	// Terminal semantic verification (real revalidation scan, metered).
	t0 := time.Now()
	again := AttestOp(w.report, roll)
	res.VerifyNS += time.Since(t0).Nanoseconds()
	if !verifyOpOut(again, w.attest) {
		res.Verdict = "REJECTED"
	}
	return w
}

// --- Modes B/B+: conventional exact cache (+evidence memory) ---

func (r *Runner) runBC(c Corpus, res *VersionResult, plus bool) {
	w := versionWork{enriched: map[string][]byte{}, verify: map[string][]byte{}, agg: map[string][]byte{}}
	sch, tax, agg := schemaD(c), taxD(c), aggD(c)
	get := func(op, key string, compute func() []byte, contract string) []byte {
		t0 := time.Now()
		ent, hit := r.Cache.Lookup(key)
		overhead(res, time.Since(t0))
		if hit {
			// Integrity check: re-hash cached bytes vs stored output digest
			// (real, cheap; catches corruption — adversarial scenario).
			t1 := time.Now()
			intact := digestOf(ent.Raw()) == ent.OutDigest
			overhead(res, time.Since(t1))
			if !intact {
				hit = false // corrupted entry: fail closed to recompute
			} else if plus {
				// B+: evidence check (contract currency + outcome status).
				if ent.Contract != contract {
					// Stale contract: re-derive + compare under current verifier.
					// Bytes are re-verified NOW, so the audit binds the
					// current contract (fresh evidence, no false reuse).
					t2 := time.Now()
					fresh := compute()
					okv := verifyOpOut(fresh, ent.Raw())
					res.VerifyNS += time.Since(t2).Nanoseconds()
					if !okv && res.Verdict == "" {
						res.Verdict = "REJECTED"
					}
					res.Reused++
					r.LastAudits = append(r.LastAudits, ReuseAudit{Op: op, Key: key,
						Contract: contract, Job: ent.JobID, Version: ent.Version, Bytes: ent.OutDigest})
					return ent.Raw()
				}
				if o, ok := r.Outcomes[ent.JobID]; !ok || o.Status != "ACCEPTED" || o.Version != ent.Version {
					hit = false // revoked/superseded evidence: recompute
				} else {
					res.Reused++
					r.LastAudits = append(r.LastAudits, ReuseAudit{Op: op, Key: key,
						Contract: ent.Contract, Job: ent.JobID, Version: ent.Version, Bytes: ent.OutDigest})
					return ent.Raw() // evidence-current: skip re-verification
				}
			} else {
				res.Reused++
				r.LastAudits = append(r.LastAudits, ReuseAudit{Op: op, Key: key,
					Contract: ent.Contract, Job: ent.JobID, Version: ent.Version, Bytes: ent.OutDigest})
				return ent.Raw()
			}
		}
		out := execOp(res, func() []byte { return compute() })
		job := r.nextJob(op)
		if op == "verify" && !r.verifyJobSeen[job] {
			r.verifyJobSeen[job] = true
			r.verifyJobs = append(r.verifyJobs, job)
			r.AllVerifyJobs = append(r.AllVerifyJobs, job)
		}
		r.Outcomes[job] = reuse.Outcome{JobID: job, Version: 1, Status: "ACCEPTED", Found: true}
		t3 := time.Now()
		_ = r.Cache.Store(CacheEntry{Key: key, BytesHex: string(out), OutDigest: digestOf(out),
			Contract: contract, JobID: job, Version: 1, Status: "ACCEPTED"})
		overhead(res, time.Since(t3))
		return out
	}
	for _, rec := range c.Records {
		rd := RecordDigest(rec)
		v := get("validate", bKey("validate", rd), func() []byte { return ValidateOp(rec) }, r.Contracts["validate"])
		_ = v
		n := get("normalize", bKey("normalize", rd, sch), func() []byte { return NormalizeOp(rec, c.Schema) }, r.Contracts["normalize"])
		e := get("extract", bKey("extract", digestOf(n), sch), func() []byte { return ExtractOp(n, c.Schema) }, r.Contracts["extract"])
		vv := get("verify", bKey("verify", rd, digestOf(e)), func() []byte { return VerifyOp(rec, e) }, r.Contracts["verify"])
		en := get("enrich", bKey("enrich", digestOf(e), tax), func() []byte { return EnrichOp(e, c.Taxonomy) }, r.Contracts["enrich"])
		w.verify[rec.ID] = vv
		w.enriched[rec.ID] = en
	}
	byShard := map[int]map[string][]byte{}
	for id, b := range w.enriched {
		s := shardOf(id, Shards)
		if byShard[s] == nil {
			byShard[s] = map[string][]byte{}
		}
		byShard[s][id] = b
	}
	for s := 0; s < Shards; s++ {
		m := byShard[s]
		if m == nil {
			m = map[string][]byte{}
		}
		sid := fmt.Sprintf("shard-%02d", s)
		parts := append([]string{sid, agg}, digestMap(m)...)
		w.agg[sid] = get("shardagg", bKey("shardagg", parts...), func() []byte { return AggregateOp(m, c.AggParam) }, r.Contracts["shardagg"])
	}
	w.combine = get("combine", bKey("combine", append([]string{agg}, digestMap(w.agg)...)...), func() []byte { return AggregateOp(w.agg, c.AggParam) }, r.Contracts["combine"])
	w.report = get("report", bKey("report", digestOf(w.combine), agg), func() []byte { return ReportOp(w.combine) }, r.Contracts["report"])
	roll := VerifyRollup(w.verify)
	// Terminal: B re-verifies every version; B+ skips on current evidence.
	termKey := bKey("attest", digestOf(w.report), digestOf([]byte(roll)))
	if plus {
		if ent, hit := r.Cache.Lookup(termKey); hit && ent.Contract == r.Contracts["attest"] {
			if o, ok := r.Outcomes[ent.JobID]; ok && o.Status == "ACCEPTED" && o.Version == ent.Version {
				res.Reused++
				r.LastAudits = append(r.LastAudits, ReuseAudit{Op: "attest", Key: termKey,
					Contract: ent.Contract, Job: ent.JobID, Version: ent.Version, Bytes: ent.OutDigest})
				res.Terminal = digestOf(ent.Raw())
				return
			}
		}
	}
	t0 := time.Now()
	ent, hit := r.Cache.Lookup(termKey)
	overhead(res, time.Since(t0))
	if hit && plus {
		// B+ outcome check on the terminal (revoked evidence → recompute).
		if o, ok := r.Outcomes[ent.JobID]; !ok || o.Status != "ACCEPTED" || o.Version != ent.Version {
			hit = false
		}
	}
	if hit {
		res.Reused++
		bound := ent.Contract
		if plus {
			// B+ re-verifies below under the current contract and rebinds
			// evidence: audit the current contract, persist the refresh.
			bound = r.Contracts["attest"]
			ent.Contract = bound
			tu := time.Now()
			_ = r.Cache.Store(ent)
			overhead(res, time.Since(tu))
		}
		r.LastAudits = append(r.LastAudits, ReuseAudit{Op: "attest", Key: termKey,
			Contract: bound, Job: ent.JobID, Version: ent.Version, Bytes: ent.OutDigest})
		w.attest = ent.Raw()
	} else {
		w.attest = execOp(res, func() []byte { return AttestOp(w.report, roll) })
		job := r.nextJob("attest")
		r.Outcomes[job] = reuse.Outcome{JobID: job, Version: 1, Status: "ACCEPTED", Found: true}
		_ = r.Cache.Store(CacheEntry{Key: termKey, BytesHex: string(w.attest), OutDigest: digestOf(w.attest),
			Contract: r.Contracts["attest"], JobID: job, Version: 1, Status: "ACCEPTED"})
	}
	// Terminal semantic verification (B always; B+ on this path too).
	t1 := time.Now()
	again := AttestOp(w.report, roll)
	res.VerifyNS += time.Since(t1).Nanoseconds()
	if !verifyOpOut(again, w.attest) {
		res.Verdict = "REJECTED"
	}
	res.Terminal = digestOf(w.attest)
}

func digestMap(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for _, id := range sortedIDs(m) {
		out = append(out, id+"="+digestOf(m[id]))
	}
	return out
}
