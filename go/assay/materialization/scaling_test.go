package materialization

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/assay/reuse"
)

// scaling_test.go: Phase 6 sparse-change scaling. N=100/1000 full fraction
// sweep {0,1,5,20,100%} × A/B/C; N=10000 sampled {0,1,100%} (time-boxed,
// documented — no 100k/1M extrapolation). Measures closure scaling,
// invalidation traversal vs recomputation, cold index/replay, storage.
// testdata/mat_scaling.json.

type ScaleRow struct {
	N        int    `json:"records"`
	Mutation string `json:"mutation"`
	Mode     string `json:"mode"`
	Exec     int    `json:"executed"`
	TotalMS  int64  `json:"total_ms"`
	StoreKB  int64  `json:"store_kb"`
	TermEq   bool   `json:"terminal_eq_a"`
}

type ScaleReport struct {
	Rows []ScaleRow `json:"rows"`
	Note string     `json:"note"`
}

func scaleModes(t *testing.T, n int, seed uint64, muts []Mutation) []ScaleRow {
	t.Helper()
	base := GenCorpus(seed, n)
	mk := func(m ModeID) *Runner {
		var cache *FileCache
		var store *reuse.Store
		var err error
		if m == ModeB || m == ModeBp {
			cache, err = OpenCache(filepath.Join(t.TempDir(), string(m)+".jsonl"))
		} else {
			store, err = reuse.Open(filepath.Join(t.TempDir(), string(m)+".jsonl"))
		}
		if err != nil {
			t.Fatal(err)
		}
		if cache != nil {
			t.Cleanup(func() { _ = cache.Close() })
		} else {
			t.Cleanup(func() { _ = store.Close() })
		}
		return NewRunner(m, cache, store)
	}
	runners := map[ModeID]*Runner{ModeA: mk(ModeA), ModeB: mk(ModeB), ModeC: mk(ModeC)}
	for _, r := range runners {
		if res := r.RunVersion(base, "v0"); res.Verdict != "ACCEPTED" {
			t.Fatalf("%s base rejected", res.Mode)
		}
	}
	var rows []ScaleRow
	for _, mut := range muts {
		c := base.WithVersion(mut)
		aTerm := runners[ModeA].RunVersion(c, mut.ID).Terminal
		for _, m := range []ModeID{ModeB, ModeC} {
			res := runners[m].RunVersion(c, mut.ID)
			rows = append(rows, ScaleRow{n, mut.ID, string(m), res.Executed,
				(res.ExecNS + res.VerifyNS + res.OverNS) / 1e6,
				res.StoreBytes / 1024, res.Terminal == aTerm})
			t.Logf("N=%d %s/%s exec=%d total=%dms store=%dKB eq=%v",
				n, mut.ID, m, res.Executed,
				(res.ExecNS+res.VerifyNS+res.OverNS)/1e6, res.StoreBytes/1024,
				res.Terminal == aTerm)
			if res.Terminal != aTerm {
				t.Errorf("N=%d %s/%s terminal mismatch", n, mut.ID, m)
			}
		}
	}
	return rows
}

func fracMutations() []Mutation {
	var out []Mutation
	for _, m := range MutationSeries() {
		switch m.ID {
		case "no-change", "chg-1pc", "chg-5pc", "chg-20pc", "full-replace":
			out = append(out, m)
		}
	}
	return out
}

func TestScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling needs full run")
	}
	rep := ScaleReport{}
	rep.Rows = append(rep.Rows, scaleModes(t, 100, 31, fracMutations())...)
	rep.Rows = append(rep.Rows, scaleModes(t, 1000, 32, fracMutations())...)
	// N=10000 sampled (documented time-box).
	rep.Rows = append(rep.Rows, scaleModes(t, 10000, 33, []Mutation{
		{ID: "no-change", Apply: func(*Corpus) {}},
		MutationSeries()[1],
		{ID: "full-replace", Apply: func(c *Corpus) {
			for i := range c.Records {
				c.Records[i].Amount += 7
				c.Records[i].Text += "|r"
			}
		}},
	})...)
	rep.Note = "N=10000 sampled to {0%,1%,100%} (per-record fsync store writes bound " +
		"full-series cost); no 100k/1M claims. Fractions mutate leading records only; " +
		"cross-scenario warm reuse is real behavior (leading-record overlap), not a bug."
	raw, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile("testdata/mat_scaling.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
