package materialization

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// matrix_test.go: Phase 4 correctness proof. Every mutation × A/B/Bp/C/D at
// N=100. Byte-grade: terminal digest equality vs A. Evidence-grade: every
// recorded reuse is checked for revoked outcomes and stale contracts.
// testdata/mat_correctness.json.

func gradeReuses(t *testing.T, mode string, audits []ReuseAudit, contracts map[string]string,
	lookup func(string) reuse.Outcome) (int, []string) {
	t.Helper()
	bad := 0
	var cases []string
	for _, a := range audits {
		if cur, ok := contracts[a.Op]; ok && a.Contract != "" && a.Contract != cur {
			bad++
			cases = append(cases, mode+":"+a.Op+":"+shortKey(a.Key)+" stale-contract")
			continue
		}
		if a.Job != "" {
			o := lookup(a.Job)
			if !o.Found || o.Status != "ACCEPTED" || o.Version != a.Version {
				bad++
				cases = append(cases, mode+":"+a.Op+":"+shortKey(a.Key)+" revoked-outcome")
			}
		}
	}
	return bad, cases
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:12]
	}
	return k
}

type matrixHarness struct {
	t      *testing.T
	base   Corpus
	rA     *Runner
	rB     *Runner
	rBp    *Runner
	rC     *Runner
	rD     *Runner
	aTerms map[string]string // mutation -> A terminal
}

func newHarness(t *testing.T, n int, seed uint64) *matrixHarness {
	t.Helper()
	base := GenCorpus(seed, n)
	mk := func(m ModeID) *Runner {
		var cache *FileCache
		var store *reuse.Store
		var err error
		if m == ModeB || m == ModeBp {
			cache, err = OpenCache(filepath.Join(t.TempDir(), string(m)+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
		} else if m == ModeC || m == ModeD {
			store, err = reuse.Open(filepath.Join(t.TempDir(), string(m)+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
		}
		return NewRunner(m, cache, store)
	}
	return &matrixHarness{t: t, base: base, rA: mk(ModeA), rB: mk(ModeB),
		rBp: mk(ModeBp), rC: mk(ModeC), rD: mk(ModeD), aTerms: map[string]string{}}
}

// warmBase runs V0 on every mode (populates caches/stores).
func (h *matrixHarness) warmBase() {
	h.t.Helper()
	for _, r := range []*Runner{h.rA, h.rB, h.rBp, h.rC, h.rD} {
		res := r.RunVersion(h.base, "v0-base")
		if res.Verdict != "ACCEPTED" {
			h.t.Fatalf("%s base rejected", res.Mode)
		}
	}
	h.aTerms["v0-base"] = h.rA.RunVersion(h.base, "v0-base").Terminal
}

func TestCorrectnessMatrix(t *testing.T) {
	h := newHarness(t, 100, 7)
	h.warmBase()
	type row struct {
		Mutation string
		Mode     string
		Exec     int
		Reused   int
		TermEq   bool
		VerdEq   bool
		False    int
	}
	var rows []row
	for _, mut := range MutationSeries() {
		c := h.base.WithVersion(mut)
		// Scenario-level setup.
		if mut.ID == "verifier-change" {
			for _, r := range []*Runner{h.rB, h.rBp, h.rC, h.rD} {
				for op := range r.Contracts {
					r.Contracts[op] = ContractFor(op, "v2")
				}
			}
		}
		var revokedJob string
		if mut.ID == "revocation" {
			// Revoke the base version's record-0 verify evidence (earliest).
			revokedJob = h.rC.FirstVerifyJob()
			if revokedJob == "" {
				t.Fatal("no verify job to revoke")
			}
			for _, r := range []*Runner{h.rC, h.rD} {
				cur := r.Outcomes[revokedJob]
				cur.Version++
				cur.Status = "REJECTED"
				cur.Found = true
				r.Outcomes[revokedJob] = cur
				r.Store.SetOutcome(cur)
				r.Store.PropagateCorrection(revokedJob, cur.Version, "REJECTED")
			}
			// B/Bp share the outcome table view for grading (B never checks).
			for _, r := range []*Runner{h.rB, h.rBp} {
				r.Outcomes[revokedJob] = h.rC.Outcomes[revokedJob]
			}
		}
		resA := h.rA.RunVersion(c, mut.ID)
		if resA.Verdict != "ACCEPTED" {
			t.Fatalf("%s: oracle rejected", mut.ID)
		}
		for _, r := range []*Runner{h.rB, h.rBp, h.rC, h.rD} {
			res := r.RunVersion(c, mut.ID)
			termEq := res.Terminal == resA.Terminal
			if res.Mode == "D" && res.Plan == "direct" {
				termEq = res.Terminal == oracleDirect(h.t, c)
			}
			verdEq := res.Verdict == resA.Verdict
			if !termEq {
				t.Errorf("%s/%s terminal mismatch", mut.ID, res.Mode)
			}
			falseN, cases := gradeReuses(t, res.Mode, r.LastAudits, r.Contracts, r.lookup)
			_ = cases
			if res.Mode == "C" || res.Mode == "D" {
				if falseN != 0 {
					t.Errorf("%s/%s FALSE REUSE: %v", mut.ID, res.Mode, cases)
				}
				if !verdEq {
					t.Errorf("%s/%s verdict %s != oracle %s", mut.ID, res.Mode, res.Verdict, resA.Verdict)
				}
			}
			rows = append(rows, row{mut.ID, res.Mode, res.Executed, res.Reused, termEq, verdEq, falseN})
			t.Logf("%s/%s exec=%d reused=%d termEq=%v verdEq=%v false=%d",
				mut.ID, res.Mode, res.Executed, res.Reused, termEq, verdEq, falseN)
		}
		if mut.ID == "verifier-change" {
			// Restore v1 contracts for subsequent scenarios? No — subsequent
			// scenarios build on mutated corpus but contracts stay rotated?
			// The series applies each mutation to BASE; contracts are runner
			// state. Reset to v1 so later scenarios test their own change.
			for _, r := range []*Runner{h.rB, h.rBp, h.rC, h.rD} {
				for op := range r.Contracts {
					r.Contracts[op] = ContractFor(op, "v1")
				}
			}
		}
	}
	raw, _ := json.MarshalIndent(rows, "", "  ")
	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile("testdata/mat_correctness.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
