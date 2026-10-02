// Command analyze performs the offline workup for one recorded pilot run:
// premise slices, SARF, staleness vs the frozen background change, L1/L2/L3
// counterfactuals, oracle + probe verdicts. No model calls. Writes
// analysis.json into the run directory. The worktree is derived from the
// stream's own absolute paths (CommonRoot); analysis is read-only except
// probe copies in temp dirs.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wiramahendra/thompson-sampling/go/assay/reasoninggoodput/livepilot"
)

func main() {
	outDir := flag.String("out", "", "recorded run directory (stream.jsonl + run.json)")
	workdirFlag := flag.String("workdir", "", "explicit worktree (for runs whose agent used relative paths)")
	task := flag.String("task", "", "frozen task id")
	change := flag.String("change", "", "frozen background change id (empty for clean runs)")
	flag.Parse()
	if *outDir == "" || *task == "" {
		fmt.Fprintln(os.Stderr, "out and task are required")
		os.Exit(2)
	}
	if err := workup(*outDir, *task, *change, *workdirFlag); err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		os.Exit(1)
	}
}

func workup(outDir, task, changeID, workdirOverride string) error {
	sess, err := livepilot.ParseStream(filepath.Join(outDir, "stream.jsonl"))
	if err != nil {
		return err
	}
	workdir := workdirOverride
	if workdir == "" {
		workdir = livepilot.CommonRoot(sess)
	}
	if st, err := os.Stat(filepath.Join(workdir, "go.mod")); err != nil || st.IsDir() {
		return fmt.Errorf("derived workdir %q has no go.mod: %v", workdir, err)
	}
	slices := livepilot.BuildSlices(sess, workdir, workdir)
	full := map[string]string{}
	for _, sl := range slices {
		for k, v := range sl.Premises {
			full[k] = v
		}
	}
	calls := make([]livepilot.CallSlice, len(slices))
	for i, sl := range slices {
		calls[i] = livepilot.CallSlice{Index: i, Premises: sl.Premises, CostNS: sl.Tokens}
	}
	sarf := livepilot.SARF(calls, full)
	var changed map[string][2]string
	if changeID != "" {
		changed = livepilot.ChangedPremises(changeID)
	}
	stale := map[string]bool{}
	var staleList []string
	for res, wit := range full {
		if pair, ok := changed[res]; ok && pair[0] == wit && pair[1] != wit {
			stale[res] = true
			staleList = append(staleList, res)
		}
	}
	l1, l2, d3, p3 := livepilot.Counterfactual(calls, stale)
	ok, _ := livepilot.GoTestGreen(workdir)
	probesPass, note := livepilot.RunProbes(workdir, task)
	rec := map[string]interface{}{
		"task": task, "change": changeID, "workdir": workdir,
		"slices": len(slices), "sarf": sarf.SARF, "opaque_frac": sarf.OpaqueFrac,
		"unknown_frac": sarf.UnknownFrac, "median_premises": sarf.MedianPremise,
		"total_reads": sarf.TotalReads, "stale": staleList,
		"l1_redo_tokens": l1, "l2_discard_tokens": l2,
		"l3_discard_tokens": d3, "l3_preserved_tokens": p3,
		"oracle_pass": ok, "probes_pass": probesPass, "probe_note": note,
		"parser": livepilot.ParserVersion,
	}
	// Per-slice audit trail (premise counts, tokens, tool calls — no content).
	var audit []map[string]interface{}
	for i, sl := range slices {
		audit = append(audit, map[string]interface{}{
			"index": i, "premises": len(sl.Premises), "tokens": sl.Tokens,
			"tool_calls": sl.ToolCalls,
		})
	}
	rec["slices_audit"] = audit
	out, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "analysis.json"), out, 0o644); err != nil {
		return err
	}
	fmt.Printf("slices=%d sarf=%.3f opaque=%.3f stale=%v oracle=%v probes=%v\n",
		len(slices), sarf.SARF, sarf.OpaqueFrac, staleList, ok, probesPass)
	return nil
}
