// Command pilot executes one live-agent premise run. See usage in main.go.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/reasoninggoodput/livepilot"
)

func runOne(task, schedule string, triggerReads int, promptFile, outDir, model string, timeoutMin int, fixtureParent string) error {
	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		return err
	}
	prompt := strings.TrimSpace(string(promptBytes))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	workdir, err := os.MkdirTemp(fixtureParent, "run-*")
	if err != nil {
		return err
	}
	if _, err := livepilot.Materialize(workdir, livepilot.FixtureAuthorDate); err != nil {
		return fmt.Errorf("materialize: %w", err)
	}
	baseHead := gitHead(workdir)
	changeID := ""
	if strings.HasPrefix(schedule, "conflicting:") {
		changeID = strings.TrimPrefix(schedule, "conflicting:")
	}
	// L0-style quiescence is expressed as schedule "clean-final:<change>":
	// pre-apply the background commit, then run without injection.
	if strings.HasPrefix(schedule, "clean-final:") {
		changeID = strings.TrimPrefix(schedule, "clean-final:")
		if _, err := livepilot.ApplyChange(workdir, changeID, livepilot.FixtureAuthorDate); err != nil {
			return fmt.Errorf("pre-apply: %w", err)
		}
		changeID = "" // no mid-run injection; already at final state
		schedule = "clean"
	}
	streamPath := filepath.Join(outDir, "stream.jsonl")
	start := time.Now()
	cmd, logf, err := launchAgent(workdir, model, prompt, streamPath)
	if err != nil {
		return err
	}
	injected := false
	var injectedAt time.Time
	inject := func() error {
		if changeID == "" {
			return nil
		}
		if _, err := livepilot.ApplyChange(workdir, changeID, livepilot.FixtureAuthorDate); err != nil {
			return err
		}
		injected = true
		injectedAt = time.Now()
		return nil
	}
	runErr := waitExit(cmd, timeoutMin, streamPath, triggerReads, inject)
	agentMS := time.Since(start).Milliseconds()
	_ = logf.Close()
	rec := RunRecord{
		RunID:    filepath.Base(outDir),
		TaskID:   task,
		Schedule: schedule,
		Model:    model,
		Parser:   livepilot.ParserVersion,
		AgentMS:  agentMS,
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}
	// Session identity + counts from the captured stream (best effort; a
	// missing/partial stream is recorded, never fabricated).
	nReads, nCalls, sessionID := summarizeStream(streamPath)
	rec.Reads = nReads
	rec.ModelCalls = nCalls
	rec.SessionID = sessionID
	rec.ChangeLanded = injected
	if injected {
		rec.ChangeLanded = true
		_ = injectedAt
	}
	// Frozen oracle: go test ./... in the final worktree.
	rec.TestsPass = goTestGreen(workdir)
	// Final diff stat vs base (informational; equivalence judged by tests).
	rec.FinalDiff = gitDiffStat(workdir, baseHead)
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		rec.Exit = exitErr.ExitCode()
	}
	out, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "run.json"), out, 0o644); err != nil {
		return err
	}
	// Copy the final worktree state digest (not full contents): changed files
	// list + HEAD, so analysis can re-derive witnesses without the bytes.
	headNow := gitHead(workdir)
	changed := gitStatus(workdir)
	meta, _ := json.MarshalIndent(map[string]string{
		"base_head": baseHead, "final_head": headNow, "status": changed,
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, "repo-meta.json"), meta, 0o644)
	return nil
}

func summarizeStream(streamPath string) (reads, calls int, session string) {
	sess, err := livepilot.ParseStream(streamPath)
	if err != nil {
		return 0, 0, ""
	}
	return len(sess.ReadEvents()), sess.ModelCalls(), sess.SessionID
}

func goTestGreen(dir string) bool {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	return cmd.Run() == nil
}

func gitHead(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitStatus(dir string) string {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var _ = filepath.Join

// gitDiffStat returns --shortstat of workdir vs base (empty when clean).
func gitDiffStat(dir, base string) string {
	cmd := exec.Command("git", "-C", dir, "diff", "--shortstat", base)
	out, err := cmd.Output()
	if err != nil {
		return "diff-unavailable"
	}
	return strings.TrimSpace(string(out))
}
