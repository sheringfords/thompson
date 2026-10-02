// Command pilot executes one live-agent premise run: materializes the
// deterministic fixture, launches OpenCode non-interactively, injects one
// frozen background commit at the trigger point, waits, validates with the
// frozen oracle, and writes a machine-readable run record. Every parameter
// comes from the frozen manifest (passed explicitly); nothing is inferred.
//
// Usage:
//
//	pilot -task local1 -schedule conflicting -trigger-reads 3 \
//	  -prompt-file task.txt -out DIR -model opencode/muse-spark-1.3-contributor-free
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	task := flag.String("task", "", "frozen task id")
	schedule := flag.String("schedule", "clean", "clean | conflicting:<change-id>")
	triggerReads := flag.Int("trigger-reads", 3, "inject after this many read events (0 = at 150s fallback only)")
	promptFile := flag.String("prompt-file", "", "frozen task prompt file")
	outDir := flag.String("out", "", "output directory")
	model := flag.String("model", "opencode/muse-spark-1.3-contributor-free", "model identifier")
	timeoutMin := flag.Int("timeout-min", 20, "agent timeout minutes")
	fixtureParent := flag.String("fixture-parent", "", "parent dir for the materialized fixture repo")
	flag.Parse()
	if *task == "" || *promptFile == "" || *outDir == "" || *fixtureParent == "" {
		fmt.Fprintln(os.Stderr, "task, prompt-file, out and fixture-parent are required")
		os.Exit(2)
	}
	if err := runOne(*task, *schedule, *triggerReads, *promptFile, *outDir, *model, *timeoutMin, *fixtureParent); err != nil {
		fmt.Fprintln(os.Stderr, "pilot:", err)
		os.Exit(1)
	}
}

// RunRecord is the machine-readable outcome of one live run.
type RunRecord struct {
	RunID        string `json:"run_id"`
	TaskID       string `json:"task_id"`
	Schedule     string `json:"schedule"`
	Model        string `json:"model"`
	Parser       string `json:"parser"`
	SessionID    string `json:"session_id"`
	Exit         int    `json:"exit"`
	AgentMS      int64  `json:"agent_ms"`
	Reads        int    `json:"reads"`
	ModelCalls   int    `json:"model_calls"`
	ChangeLanded bool   `json:"change_landed"`
	TestsPass    bool   `json:"tests_pass"`
	FinalDiff    string `json:"final_diff_stat"`
	Error        string `json:"error,omitempty"`
}

// launchAgent starts opencode run in dir, streaming JSONL to streamPath.
func launchAgent(dir, model, prompt, streamPath string) (*exec.Cmd, *os.File, error) {
	f, err := os.Create(streamPath)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command("opencode", "run", "--format", "json", "--model", model, prompt)
	cmd.Dir = dir
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, nil, err
	}
	return cmd, f, nil
}

// countReadEvents tails a JSONL stream counting file-read tool parts.
func countReadEvents(streamPath string) (int, error) {
	f, err := os.Open(streamPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		part, _ := obj["part"].(map[string]interface{})
		if part == nil {
			continue
		}
		ptype, _ := part["type"].(string)
		if !strings.Contains(ptype, "tool") {
			continue
		}
		tool, _ := part["tool"].(string)
		if tool == "" {
			tool, _ = part["name"].(string)
		}
		b, _ := json.Marshal(part["input"])
		lower := strings.ToLower(tool + " " + string(b))
		if strings.Contains(lower, "read") || strings.Contains(lower, ".go") {
			n++
		}
	}
	return n, sc.Err()
}

// waitExit waits with polling for trigger injection.
func waitExit(cmd *exec.Cmd, timeoutMin int, pollPath string, threshold int, inject func() error) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	injected := false
	deadline := time.Now().Add(time.Duration(timeoutMin) * time.Minute)
	fallback := time.Now().Add(150 * time.Second)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-tick.C:
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				return fmt.Errorf("agent timeout after %dm", timeoutMin)
			}
			if !injected && inject != nil {
				fire := false
				if threshold > 0 {
					if n, err := countReadEvents(pollPath); err == nil && n >= threshold {
						fire = true
					}
				}
				if !fire && time.Now().After(fallback) {
					fire = true
				}
				if fire {
					injected = true
					if err := inject(); err != nil {
						return fmt.Errorf("inject: %w", err)
					}
				}
			}
		}
	}
}

var _ = filepath.Join
