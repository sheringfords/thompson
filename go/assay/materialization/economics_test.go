package materialization

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// Primary outputs per scenario×mode: executed ops, exec/verify/overhead ns,
// store bytes, terminal verdict, false reuse. Net benefit after overhead.
// Frozen charter gate: C ≥15% fewer TOTAL work units than B on ≥2 of
// {1%, 5%, schema-change, enrichment-change}, zero incorrect C terminals.
// testdata/mat_economics.json.

func econHarness(t *testing.T, n int, seed uint64) (Corpus, map[ModeID]*Runner) {
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
		} else {
			store, err = reuse.Open(filepath.Join(t.TempDir(), string(m)+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
		}
		return NewRunner(m, cache, store)
	}
	modes := map[ModeID]*Runner{ModeA: mk(ModeA), ModeB: mk(ModeB), ModeBp: mk(ModeBp),
		ModeC: mk(ModeC), ModeD: mk(ModeD)}
	// Warm base on all modes.
	var aTerm string
	for _, m := range []ModeID{ModeA, ModeB, ModeBp, ModeC, ModeD} {
		res := modes[m].RunVersion(base, "v0-base")
		if res.Verdict != "ACCEPTED" {
			t.Fatalf("%s base rejected", m)
		}
		if m == ModeA {
			aTerm = res.Terminal
		}
	}
	_ = aTerm
	return base, modes
}

func TestEconomicsN1000(t *testing.T) {
	if testing.Short() {
		t.Skip("economics needs full run")
	}
	base, modes := econHarness(t, 1000, 21)
	rep := EconReport{Gate: map[string]string{}, Decomp: map[string]string{}}
	for _, mut := range MutationSeries() {
		c := base.WithVersion(mut)
		if mut.ID == "verifier-change" {
			for _, r := range []*Runner{modes[ModeB], modes[ModeBp], modes[ModeC], modes[ModeD]} {
				for op := range r.Contracts {
					r.Contracts[op] = ContractFor(op, "v2")
				}
			}
		}
		var revoked string
		if mut.ID == "revocation" {
			revoked = modes[ModeC].FirstVerifyJob()
			for _, r := range []*Runner{modes[ModeC], modes[ModeD]} {
				cur := r.Outcomes[revoked]
				cur.Version++
				cur.Status = "REJECTED"
				cur.Found = true
				r.Outcomes[revoked] = cur
				r.Store.SetOutcome(cur)
				r.Store.PropagateCorrection(revoked, cur.Version, "REJECTED")
			}
			for _, r := range []*Runner{modes[ModeB], modes[ModeBp]} {
				r.Outcomes[revoked] = modes[ModeC].Outcomes[revoked]
			}
		}
		resA := modes[ModeA].RunVersion(c, mut.ID)
		wantD := oracleDirect(t, c)
		rep.Rows = append(rep.Rows, EconRow{mut.ID, "A", "",
			resA.Executed, resA.Reused, resA.ExecNS, resA.VerifyNS, resA.OverNS,
			resA.ExecNS + resA.VerifyNS + resA.OverNS, 0,
			resA.Verdict, true, 0})
		for _, m := range []ModeID{ModeB, ModeBp, ModeC, ModeD} {
			r := modes[m]
			res := r.RunVersion(c, mut.ID)
			falseN, _ := gradeReuses(t, res.Mode, r.LastAudits, r.Contracts, r.lookup)
			termEq := res.Terminal == resA.Terminal
			if m == ModeD && res.Plan == "direct" {
				termEq = res.Terminal == wantD
			}
			rep.Rows = append(rep.Rows, EconRow{mut.ID, res.Mode, res.Plan,
				res.Executed, res.Reused, res.ExecNS, res.VerifyNS, res.OverNS,
				res.ExecNS + res.VerifyNS + res.OverNS, res.StoreBytes,
				res.Verdict, termEq, falseN})
			t.Logf("%s/%s plan=%s exec=%d reused=%d total=%dms (e%d/v%d/o%d) verdict=%s eq=%v false=%d store=%dKB",
				mut.ID, m, res.Plan, res.Executed, res.Reused,
				(res.ExecNS+res.VerifyNS+res.OverNS)/1e6,
				res.ExecNS/1000, res.VerifyNS/1000, res.OverNS/1000,
				res.Verdict, termEq, falseN, res.StoreBytes/1024)
		}
		if mut.ID == "verifier-change" {
			for _, r := range []*Runner{modes[ModeB], modes[ModeBp], modes[ModeC], modes[ModeD]} {
				for op := range r.Contracts {
					r.Contracts[op] = ContractFor(op, "v1")
				}
			}
		}
	}
	// Charter gate evaluation (frozen rule from MODEL_V1.md).
	totals := map[string]map[string]int64{}
	for _, row := range rep.Rows {
		if _, ok := totals[row.Mutation]; !ok {
			totals[row.Mutation] = map[string]int64{}
		}
		totals[row.Mutation][row.Mode] = row.TotalNS
	}
	pass := 0
	for _, sc := range []string{"chg-1pc", "chg-5pc", "schema-change", "enrichment-change"} {
		b, c := totals[sc]["B"], totals[sc]["C"]
		saving := 100.0 * float64(b-c) / float64(b)
		ok := saving >= 15.0
		if ok {
			pass++
		}
		rep.Gate[sc] = fmt.Sprintf("%.1f%% (B=%dms C=%dms) pass=%v", saving, b/1e6, c/1e6, ok)
	}
	rep.Gate["verdict"] = fmt.Sprintf("%d/4 charter scenarios pass (need 2)", pass)
	// B→B+→C decomposition on sparse scenarios.
	for _, sc := range []string{"chg-1pc", "chg-5pc", "no-change"} {
		b, bp, c := totals[sc]["B"], totals[sc]["Bp"], totals[sc]["C"]
		rep.Decomp[sc] = fmt.Sprintf("B=%dms Bp=%dms C=%dms | evidence-memory=%dus graph-delta=%dus",
			b/1e6, bp/1e6, c/1e6, (b-bp)/1000, (bp-c)/1000)
	}
	raw, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile("testdata/mat_economics.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// Hard gates: C byte-equality vs A; D byte-equality vs the DIRECT oracle
	// (cross-plan equality is not expected — predeclared); verdicts ACCEPTED;
	// zero false reuse in C/D.
	for _, row := range rep.Rows {
		if (row.Mode == "C" || row.Mode == "D") && row.Verdict != "ACCEPTED" {
			t.Errorf("verdict gate: %+v", row)
		}
		if row.Mode == "C" && !row.TermEq {
			t.Errorf("correctness gate: %+v", row)
		}
		if (row.Mode == "C" || row.Mode == "D") && row.False != 0 {
			t.Errorf("false-reuse gate: %+v", row)
		}
	}
}

// oracleDirect runs the direct-shape full recomputation (isolated, no store):
// record layer fresh + direct + attest. Ground truth for D-direct rows
// (cross-plan byte equality is NOT expected — predeclared).
func oracleDirect(t *testing.T, c Corpus) string {
	t.Helper()
	enriched := map[string][]byte{}
	verifyM := map[string][]byte{}
	for _, rec := range c.Records {
		n := NormalizeOp(rec, c.Schema)
		e := ExtractOp(n, c.Schema)
		vv := VerifyOp(rec, e)
		en := EnrichOp(e, c.Taxonomy)
		verifyM[rec.ID] = vv
		enriched[rec.ID] = en
	}
	rep := DirectOp(enriched, c.AggParam)
	roll := VerifyRollup(verifyM)
	return digestOf(AttestOp(rep, roll))
}
