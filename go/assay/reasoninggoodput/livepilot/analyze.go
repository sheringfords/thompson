// Offline run analysis: from a recorded run (stream + repo-meta + schedule)
// to SARF, L1/L2/L3 counterfactuals, and oracle verdicts. No model calls.
// Staleness is evaluated against the frozen background change's blob OIDs
// (base vs post), never against agent-written state.
package livepilot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// TaskProbes maps frozen task IDs to overlay probe sources (compiled into a
// COPY of the final worktree; the agent never sees them).
func TaskProbes() map[string]string {
	return map[string]string{
		"local1": `package calc

import "testing"

func TestProbeLocal1(t *testing.T) {
	if got := TotalWithTax(100.0); got != 109.0 {
		t.Fatalf("probe: got %v want 109.0", got)
	}
}
`,
		"local2": `package format

import "testing"

func TestProbeLocal2(t *testing.T) {
	if got := FormatUSD(1); got != "USD 1.00" {
		t.Fatalf("probe: got %q", got)
	}
}
`,
		"cross1": `package api

import (
	"testing"

	"ledgercalc/store"
)

func TestProbeCross1(t *testing.T) {
	if !ValidateItem(store.Item{Code: "X"}) {
		t.Fatal("probe: want true")
	}
	if ValidateItem(store.Item{}) {
		t.Fatal("probe: want false")
	}
}
`,
		"cross2": `package calc

import "testing"

func TestProbeCross2(t *testing.T) {
	if got := Discount(200, 25, 50); got != 150 {
		t.Fatalf("probe: got %v want 150", got)
	}
	if got := Discount(40, 25, 50); got != 50 {
		t.Fatalf("probe: got %v want 50", got)
	}
}
`,
		"disjoint1": `package calc

import "testing"

func TestProbeDisjoint1Sum(t *testing.T) {
	if got := Sum([]float64{-5, 10}); got != 10 {
		t.Fatalf("probe: got %v want 10", got)
	}
}
`,
		"disjoint1store": `package store

import "testing"

func TestProbeDisjoint1Round(t *testing.T) {
	// math.Round half-up: 2.675*100=267.5 rounds to 268.
	if got := LineTotal(Item{Price: 2.675}, 1); got != 2.68 {
		t.Fatalf("probe: got %v want 2.68", got)
	}
}
`,
		"disjoint2": `package api

import (
	"strings"
	"testing"
)

func TestProbeDisjoint2Name(t *testing.T) {
	if ValidateName(strings.Repeat("a", 41)) {
		t.Fatal("probe: want false for 41 chars")
	}
}
`,
		"disjoint2format": `package format

import (
	"strings"
	"testing"
)

func TestProbeDisjoint2Paren(t *testing.T) {
	got := FormatUSD(-1)
	if !strings.HasPrefix(got, "(") || !strings.HasSuffix(got, ")") || !strings.Contains(got, "1.00") {
		t.Fatalf("probe: got %q", got)
	}
}
`,
		"opaque1": `package calc

import "testing"

func TestProbeOpaque1(t *testing.T) {
	if got, want := CartTotal([]float64{100, 100, 100}, 10), TotalWithTax(270); got != want {
		t.Fatalf("probe: got %v want %v", got, want)
	}
}
`,
		"opaque2": `package calc

import "testing"

func TestProbeOpaque2(t *testing.T) {
	if got := Discount(2048, 25); got != 1536 {
		t.Fatalf("probe: got %v want 1536", got)
	}
}
`,
	}
}

// ProbesForTask returns the overlay probe file set for a task: map of
// package dir -> probe source (multiple probes per task allowed).
func ProbesForTask(task string) map[string]string {
	all := TaskProbes()
	out := map[string]string{}
	pkgOf := map[string]string{
		"local1": "calc", "local2": "format", "cross1": "api",
		"cross2": "calc", "disjoint1": "calc", "disjoint2": "api",
		"opaque1": "calc", "opaque2": "calc",
	}
	if src, ok := all[task]; ok {
		out[pkgOf[task]+"/probe_assay_test.go"] = src
	}
	if task == "disjoint1" {
		out["store/probe_assay_test.go"] = all["disjoint1store"]
	}
	if task == "disjoint2" {
		out["format/probe_assay_test.go"] = all["disjoint2format"]
	}
	return out
}

// RunProbes copies workdir to temp, overlays probe files, and runs
// `go test` for the probe tests. Returns pass/fail + output.
func RunProbes(workdir, task string) (bool, string) {
	tmp, err := os.MkdirTemp("", "probe-*")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(tmp)
	cp := exec.Command("cp", "-r", workdir+"/.", tmp+"/")
	if out, err := cp.CombinedOutput(); err != nil {
		return false, fmt.Sprintf("copy: %v %s", err, out)
	}
	probes := ProbesForTask(task)
	if len(probes) == 0 {
		return false, "no probes for task"
	}
	names := []string{}
	for rel := range probes {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		full := filepath.Join(tmp, rel)
		if err := os.WriteFile(full, []byte(probes[rel]), 0o644); err != nil {
			return false, err.Error()
		}
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	_ = out
	// Probe verdict: run only the Probe tests for a clean signal.
	cmd2 := exec.Command("go", "test", "-run", "TestProbe", "./...")
	cmd2.Dir = tmp
	out2, err2 := cmd2.CombinedOutput()
	if err2 != nil {
		// Probe compile failure = INCONCLUSIVE-SIGNATURE (documented rule).
		if strings.Contains(string(out2), "undefined:") || strings.Contains(string(out2), "cannot") {
			return false, "INCONCLUSIVE-SIGNATURE: " + strings.TrimSpace(string(out2))
		}
		return false, strings.TrimSpace(string(out2))
	}
	return true, strings.TrimSpace(string(out2))
}

// GoTestGreen runs the frozen oracle suite in dir.
func GoTestGreen(dir string) (bool, string) {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return err == nil, strings.TrimSpace(string(out))
}

// RunAnalysis is the full offline workup for one recorded run.
type RunAnalysis struct {
	RunID       string
	Slices      int
	SARF        float64
	OpaqueFrac  float64
	UnknownFrac float64
	MedianPrem  int
	TotalReads  int
	Stale       []string
	L1Redo      int64
	L2Discard   int64
	L3Discard   int64
	L3Preserved int64
	OraclePass  bool
	ProbesPass  bool
	ProbeNote   string
}

// AnalyzeRun performs premise assignment, SARF, staleness, counterfactuals
// and oracle checks for a recorded run directory (stream.jsonl +
// repo-meta.json inside) against a materialized repoDir worktree.
func AnalyzeRun(outDir, repoDir, task string, changed map[string][2]string) (*RunAnalysis, error) {
	sess, err := ParseStream(filepathJoin(outDir, "stream.jsonl"))
	if err != nil {
		return nil, err
	}
	workPrefix := CommonRoot(sess)
	slices := BuildSlices(sess, repoDir, workPrefix)
	full := map[string]string{}
	for _, sl := range slices {
		for k, v := range sl.Premises {
			full[k] = v
		}
	}
	calls := make([]CallSlice, len(slices))
	for i, sl := range slices {
		calls[i] = CallSlice{Index: i, Premises: sl.Premises, CostNS: sl.Tokens}
	}
	sarf := SARF(calls, full)
	stale := map[string]bool{}
	var staleList []string
	for res, wit := range full {
		if pair, ok := changed[res]; ok && pair[0] == wit && pair[1] != wit {
			stale[res] = true
			staleList = append(staleList, res)
		}
	}
	sort.Strings(staleList)
	l1, l2, d3, p3 := Counterfactual(calls, stale)
	ok, _ := GoTestGreen(repoDir)
	probesPass, note := RunProbes(repoDir, task)
	return &RunAnalysis{
		Slices: len(slices), SARF: sarf.SARF, OpaqueFrac: sarf.OpaqueFrac,
		UnknownFrac: sarf.UnknownFrac, MedianPrem: sarf.MedianPremise,
		TotalReads: sarf.TotalReads, Stale: staleList,
		L1Redo: l1, L2Discard: l2, L3Discard: d3, L3Preserved: p3,
		OraclePass: ok, ProbesPass: probesPass, ProbeNote: note,
	}, nil
}

func filepathJoin(a, b string) string { return filepath.Join(a, b) }
