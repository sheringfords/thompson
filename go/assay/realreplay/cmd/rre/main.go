// Command rre drives the real-replay economics assay. Every opencode
// invocation is counted in <out>/invocations.jsonl against the frozen cap.
//
//	rre -out DIR -work DIR smoke
//	rre -out DIR -work DIR probes
//	rre -out DIR -work DIR [-rep b] git
//	rre -out DIR -work DIR [-rep b] [-h1x] http
//	rre -out DIR decide
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/realreplay"
)

func main() {
	out := flag.String("out", "", "evidence directory (docs/research/real-replay-economics)")
	work := flag.String("work", "/private/tmp/rre-work", "scratch root for authorities and workspaces")
	rep := flag.String("rep", "", "repetition suffix (\"\" or \"b\")")
	h1x := flag.Bool("h1x", false, "also run the H1x second-change case")
	only := flag.String("only", "", "limit a family to one mutation (repetitions: G1 or H1)")
	flag.Parse()
	if *out == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: rre -out DIR [-work DIR] [-rep b] [-h1x] smoke|probes|git|http|decide")
		os.Exit(2)
	}
	absOut, err := filepath.Abs(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rre:", err)
		os.Exit(2)
	}
	absWork, err := filepath.Abs(*work)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rre:", err)
		os.Exit(2)
	}
	r := &realreplay.Runner{Work: absWork, Out: absOut, Cap: 20, Timeout: 20 * time.Minute, ResumeMessage: "Continue."}
	ctx := context.Background()
	if err := run(ctx, r, flag.Arg(0), *rep, *h1x, *only); err != nil {
		fmt.Fprintln(os.Stderr, "rre:", err)
		os.Exit(1)
	}
}

func save(path string, v interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func run(ctx context.Context, r *realreplay.Runner, cmd, rep string, h1x bool, only string) error {
	switch cmd {
	case "smoke":
		rp, err := r.Smoke(ctx)
		if rp != nil {
			_ = save(filepath.Join(r.Out, "smoke", "report.json"), rp)
		}
		return err
	case "smoke2":
		rp, err := r.SmokeResume(ctx)
		if rp != nil {
			_ = save(filepath.Join(r.Out, "smoke", "report-smoke2.json"), rp)
		}
		return err
	case "probes":
		ps, err := realreplay.CommitRaceProbes(filepath.Join(r.Work, "probes"), "127.0.0.1:18742")
		if err != nil {
			return err
		}
		return save(filepath.Join(r.Out, "results", "commit-race-probes.json"), ps)
	case "git":
		muts := []string{"G1", "G2", "G3"}
		if only != "" {
			muts = []string{only}
		}
		_, err := r.GitFamily(ctx, rep, muts)
		return err
	case "http":
		muts := []string{"H1", "H2"}
		if only != "" {
			muts = []string{only}
		}
		_, _, err := r.HTTPFamily(ctx, rep, muts, h1x)
		return err
	case "decide":
		return decide(r.Out)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func decide(out string) error {
	files, _ := filepath.Glob(filepath.Join(out, "results", "*.json"))
	sort.Strings(files)
	var scs []realreplay.Scenario
	var gates []realreplay.Gates
	guardOK := true
	var guardWhy []string
	var adversarial []realreplay.Outcome
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if filepath.Base(f) == "commit-race-probes.json" {
			var ps []realreplay.RaceProbe
			if err := json.Unmarshal(b, &ps); err != nil {
				return err
			}
			for _, p := range ps {
				if !p.Rejected {
					guardOK = false
					guardWhy = append(guardWhy, "race probe accepted: "+p.Name)
				}
			}
			continue
		}
		var o realreplay.Outcome
		if err := json.Unmarshal(b, &o); err != nil {
			return err
		}
		s := o.Scenario
		if len(s.ID) >= 3 && s.ID[:3] == "H1x" {
			adversarial = append(adversarial, o)
			if !(s.R2Rounds == 2 && s.R2Committed && s.R2Oracle && s.MissedStale == 0) {
				guardOK = false
				guardWhy = append(guardWhy, "H1x: second change did not yield a revalidated correct commit")
			}
			continue
		}
		if s.Mutation != "G3" && len(o.S0Valid.Stale)+len(o.S0Valid.Unknown) == 0 {
			guardOK = false
			guardWhy = append(guardWhy, s.ID+": semantic mutation not detected stale")
		}
		scs = append(scs, s)
		gates = append(gates, o.Gates)
	}
	if len(files) == 0 {
		return fmt.Errorf("no results")
	}
	d, why := realreplay.Decide(scs, gates, guardOK)
	return save(filepath.Join(out, "decision.json"), map[string]interface{}{
		"decision": d, "why": why, "guard_ok": guardOK, "guard_notes": guardWhy,
		"scenarios": len(scs), "adversarial": len(adversarial),
	})
}
