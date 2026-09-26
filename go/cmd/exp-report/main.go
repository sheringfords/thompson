// Command exp-report renders the offline experiment readout from treatment
// ledgers. It reads assignments + outcomes per treatment, enforces the
// maturation window, and prints metrics, comparison CIs, sensitivities, and
// gate verdicts. It never learns, never writes to ledgers, and exits 2 when
// the data are not ready for analysis.
//
// Usage:
//
//	exp-report --root ./exp --treatments t0,t1,t2 --baseline t0 --candidate t2 \
//	  --maturation 24h --min-jobs 1000 --format json
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// refused signals not-ready data (exit 2) as opposed to a usage error (exit 1).
type refused struct{ reason string }

func (e *refused) Error() string { return "analysis refused: " + e.reason }

func run(args []string) error {
	fs := flag.NewFlagSet("exp-report", flag.ContinueOnError)
	root := fs.String("root", "", "experiment root dir (subdirs per treatment)")
	treatments := fs.String("treatments", "t0,t1,t2", "comma-separated treatment IDs")
	baseline := fs.String("baseline", "t0", "baseline treatment for comparisons")
	candidate := fs.String("candidate", "t2", "candidate treatment for comparisons")
	maturation := fs.Duration("maturation", 24*time.Hour, "common outcome-maturation window")
	nowStr := fs.String("now", "", "analysis clock RFC3339 (default: current time)")
	minJobs := fs.Int("min-jobs", 1000, "minimum matured jobs per treatment")
	censorGate := fs.Float64("censor-gate", 0.05, "max censored fraction per treatment")
	qualityFloor := fs.Float64("quality-floor", 0.95, "minimum winner accept rate")
	minEffect := fs.Float64("min-effect", 0.15, "minimum relative improvement (commercial bar)")
	bootstrap := fs.Int("bootstrap", 2000, "bootstrap resamples per comparison")
	seed := fs.Uint64("seed", 0xE1C, "bootstrap seed")
	format := fs.String("format", "text", "text|json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("missing --root")
	}
	now := time.Now().UTC()
	if *nowStr != "" {
		t, err := time.Parse(time.RFC3339, *nowStr)
		if err != nil {
			return fmt.Errorf("bad --now: %w", err)
		}
		now = t
	}
	var txNames []string
	for _, n := range strings.Split(*treatments, ",") {
		if n = strings.TrimSpace(n); n != "" {
			txNames = append(txNames, n)
		}
	}
	var others []string
	for _, n := range txNames {
		if n != *baseline && n != *candidate {
			others = append(others, n)
		}
	}

	var allAssign []harness.Assignment
	var allEvents []outcome.OutcomeEvent
	for _, n := range txNames {
		as, evs, err := harness.LoadTreatmentDir(*root + "/" + n)
		if err != nil {
			return fmt.Errorf("treatment %s: %w", n, err)
		}
		allAssign = append(allAssign, as...)
		allEvents = append(allEvents, evs...)
	}
	cfg := harness.ReportConfig{
		Maturation: *maturation, Now: now,
		MinJobs: *minJobs, CensorGate: *censorGate, QualityFloor: *qualityFloor,
		MinEffect: *minEffect, BootstrapN: *bootstrap, BootstrapSeed: *seed,
	}
	rep, err := buildReport(allAssign, allEvents, txNames, *baseline, *candidate, others, cfg)
	if err != nil {
		return err
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case "text":
		printText(rep)
		return nil
	default:
		return fmt.Errorf("bad --format %q", *format)
	}
}

// buildReport enforces readiness (all jobs matured), then analyzes.
func buildReport(allAssign []harness.Assignment, allEvents []outcome.OutcomeEvent, txNames []string, baseline, candidate string, others []string, cfg harness.ReportConfig) (harness.Report, error) {
	if len(allAssign) == 0 {
		return harness.Report{}, &refused{"no assigned jobs"}
	}

	// Do not finalize until the final assigned job has matured.
	latest := allAssign[0].AssignedAt
	for _, a := range allAssign[1:] {
		if a.AssignedAt > latest {
			latest = a.AssignedAt
		}
	}
	lastAssigned, err := time.Parse(time.RFC3339Nano, latest)
	if err != nil {
		return harness.Report{}, fmt.Errorf("bad assigned_at: %w", err)
	}
	if cfg.Now.Before(lastAssigned.Add(cfg.Maturation)) {
		return harness.Report{}, &refused{fmt.Sprintf("final job assigned %s matures at %s (now %s)",
			lastAssigned.Format(time.RFC3339), lastAssigned.Add(cfg.Maturation).Format(time.RFC3339),
			cfg.Now.Format(time.RFC3339))}
	}

	records := harness.MatureJobs(allAssign, allEvents, cfg.Now, cfg.Maturation)
	baseWinner := argminPrimary(records, txNames)
	half, dbl := harness.MaturityStability(baseWinner, allAssign, allEvents, cfg.Now, cfg.Maturation, txNames)
	return harness.Analyze(records, cfg, baseline, candidate, others, half, dbl), nil
}

// argminPrimary is the base-cutoff winner by primary metric.
func argminPrimary(records []harness.JobRecord, txNames []string) string {
	byTx := map[string][]harness.JobRecord{}
	for _, r := range records {
		if r.Matured {
			byTx[r.Treatment] = append(byTx[r.Treatment], r)
		}
	}
	best, bestP := "", 0.0
	first := true
	for _, n := range txNames {
		var cost, acc float64
		for _, r := range byTx[n] {
			if r.HasOutcome && r.FullyMetered && (r.Status == outcome.StatusAccepted || r.Status == outcome.StatusRejected) {
				cost += r.CostMetered
				if r.Accepted {
					acc++
				}
			}
		}
		if acc == 0 {
			continue
		}
		if p := cost / acc; first || p < bestP {
			best, bestP, first = n, p, false
		}
	}
	return best
}

func printText(rep harness.Report) {
	fmt.Printf("experiment report (maturation %s, now %s)\n", rep.Maturation, rep.Now)
	for _, g := range rep.Gates {
		status := "PASS"
		if !g.Pass {
			status = "FAIL"
		}
		fmt.Printf("gate %-20s %s %s\n", g.Name, status, g.Detail)
	}
	for name, st := range rep.Treatments {
		fmt.Printf("%s: matured=%d accepted=%d rejected=%d unknown=%d unresolved=%d censored=%.3f primary=%.4f metered=%.2f accept_rate=%.3f\n",
			name, st.Matured, st.Accepted, st.Rejected, st.Unknown, st.Unresolved,
			st.CensoredFraction, st.Primary, st.MeteredShare, st.AcceptRate)
	}
	for _, c := range rep.Comparisons {
		fmt.Printf("%s: diff=%+.4f rel=%.3f ci95=[%+.4f,%+.4f] wins=%v bar=%v\n",
			c.Pair, c.Diff, c.RelImprovement, c.CILow, c.CIHigh, c.Wins, c.MeetsBar)
	}
	fmt.Printf("sensitivity: worst_case_wins=%v missing=[%+.4f,%+.4f] half_stable=%v double_stable=%v corrections=%s\n",
		rep.Sensitivity.WorstCaseCensoringWins, rep.Sensitivity.MissingCostLow,
		rep.Sensitivity.MissingCostHigh, rep.Sensitivity.HalfMaturityStable,
		rep.Sensitivity.DoubleMaturityStable, rep.Sensitivity.CorrectionAsymmetry)
	fmt.Printf("verdict: %s\n", rep.Verdict)
	for _, r := range rep.Reasons {
		fmt.Printf("  - %s\n", r)
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var r *refused
		if errors.As(err, &r) {
			fmt.Fprintf(os.Stderr, "exp-report: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "exp-report: %v\n", err)
		os.Exit(1)
	}
}

// exitCode maps errors to process exit codes (2 = refused/not-ready).
func exitCode(err error) int {
	var r *refused
	if errors.As(err, &r) {
		return 2
	}
	return 1
}
