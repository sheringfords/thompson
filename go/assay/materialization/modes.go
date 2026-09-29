package materialization

import (
	"fmt"
	"sort"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// modes.go: A/B/B+/C/D runners over versioned corpora.
//
// Fairness (frozen): B keys every op by its immediate input bytes + config
// (credible conventional cache; documented evidence gaps: no verifier
// contract, executor, outcome lineage or graph in keys). B verifies hits by
// integrity (digest compare, metered as overhead) plus terminal semantic
// verification per version (required to produce the version verdict).
// B+ adds verification-evidence memory (still no graph). C adds the
// dependency graph + evidence binding. D adds plan selection over C.
// Work accounting uses MEASURED ns throughout (exec / verify / overhead).

// ModeID names a treatment.
type ModeID string

const (
	ModeA  ModeID = "A"
	ModeB  ModeID = "B"
	ModeBp ModeID = "Bp"
	ModeC  ModeID = "C"
	ModeD  ModeID = "D"
)

// VersionResult is the auditable per-version record.
type VersionResult struct {
	Mode       string   `json:"mode"`
	Mutation   string   `json:"mutation"`
	Records    int      `json:"records"`
	Executed   int      `json:"executed_ops"`
	Reused     int      `json:"reused_ops"`
	ExecNS     int64    `json:"exec_ns"`
	VerifyNS   int64    `json:"verify_ns"`
	OverNS     int64    `json:"overhead_ns"`
	StoreBytes int64    `json:"store_bytes"`
	Terminal   string   `json:"terminal_digest"`
	Verdict    string   `json:"verdict"`
	FalseReuse int      `json:"false_reuse"`
	FalseCases []string `json:"false_reuse_cases"`
	StaleN     int      `json:"stale_count"`
	InvalidN   int      `json:"invalid_count"`
	Plan       string   `json:"plan,omitempty"`
}

// Runner holds cross-version state per mode.
type Runner struct {
	Mode      ModeID
	Cache     *FileCache   // B/B+
	Store     *reuse.Store // C/D
	Outcomes  map[string]reuse.Outcome
	Bodies    map[string][]byte
	Contracts map[string]string // op -> current contract digest
	JobSeq    int
	CalibNS   map[string]float64 // op -> calibrated mean ns (quotes only)
	// Per-version audit trail (reset each RunVersion).
	LastAudits    []ReuseAudit
	verifyJobs    []string
	verifyJobSeen map[string]bool
	// AllVerifyJobs accumulates every verify-op job across runs (evidence
	// history for revocation scenarios).
	AllVerifyJobs []string
}

// LastVerifyJob0 returns the first verify-op job of the last run.
func (r *Runner) LastVerifyJob0() string {
	if len(r.verifyJobs) == 0 {
		return ""
	}
	return r.verifyJobs[0]
}

// FirstVerifyJob returns the earliest verify-op job on record.
func (r *Runner) FirstVerifyJob() string {
	if len(r.AllVerifyJobs) == 0 {
		return ""
	}
	return r.AllVerifyJobs[0]
}

func digestOf(b []byte) string { return reuse.DigestBytes(b) }

// ReuseAudit is one reuse event for evidence-grade grading.
type ReuseAudit struct {
	Op       string
	Key      string
	Contract string // contract digest bound at write (B/B+) or stored dep (C/D)
	Job      string
	Version  uint64
	Bytes    string // artifact/bytes digest
}

func (r *Runner) nextJob(node string) string {
	r.JobSeq++
	return fmt.Sprintf("%s/j%d", node, r.JobSeq)
}

func (r *Runner) lookup(job string) reuse.Outcome {
	if o, ok := r.Outcomes[job]; ok {
		return o
	}
	return reuse.Outcome{}
}

// verifyOpOut compares recomputed output (constant-time-ish, metered by caller).
func verifyOpOut(want, got []byte) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

// recordKeys returns B-style keys + C-style key builder inputs for one record.
type recordWork struct {
	rec       Record
	validateB []byte
	normB     []byte
	extB      []byte
	verifyB   []byte
	enrichB   []byte
}

// execOp times one op execution.
func execOp(res *VersionResult, fn func() []byte) []byte {
	t0 := time.Now()
	out := fn()
	res.ExecNS += time.Since(t0).Nanoseconds()
	res.Executed++
	return out
}

// overhead times hashing/lookup/traversal/persistence.
func overhead(res *VersionResult, d time.Duration) {
	res.OverNS += d.Nanoseconds()
}

// bKey builds the conventional key for an op invocation.
func bKey(op string, parts ...string) string { return CacheKey(op, parts...) }

// cKey builds the V1 ExecutionKey for a materialized computation.
func (r *Runner) cKey(op, opVer, input string, deps []reuse.Dep, contract string) reuse.ExecutionKey {
	return reuse.ExecutionKey{
		Operation: op, OperationVersion: opVer, InputDigest: input,
		Deps: deps, Executor: "mat/1.0", EnvDigest: reuse.DigestString(""),
		VerifierContract: contract, PolicyDigest: reuse.DigestString("mat-default"),
	}
}

func sortedIDs(m map[string][]byte) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
