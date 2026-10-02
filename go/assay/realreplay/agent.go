package realreplay

// OpenCode adapter (assay-only). Everything OpenCode-specific lives here
// and in continuation.go; the premise/validation core does not depend on
// it.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Model is the only model this assay may invoke.
const Model = "opencode/muse-spark-1.3-contributor-free"

// Invocation records one `opencode run` process (every process counts
// toward the run cap, including ones that fail before a model step).
type Invocation struct {
	Label     string `json:"label"`
	Session   string `json:"session"`
	Resume    bool   `json:"resume"`
	Exit      int    `json:"exit"`
	WallMS    int64  `json:"wall_ms"`
	TimedOut  bool   `json:"timed_out"`
	StreamOut string `json:"stream"`
}

// RunAgent launches `opencode run` in ws, streaming JSON events to
// streamPath. session != "" resumes that session with message.
func RunAgent(ctx context.Context, ws, message, session, streamPath string, timeout time.Duration) (Invocation, error) {
	inv := Invocation{Session: session, Resume: session != "", StreamOut: streamPath}
	f, err := os.Create(streamPath)
	if err != nil {
		return inv, err
	}
	defer f.Close()
	args := []string{"run", "--format", "json", "--model", Model, "--auto"}
	if session != "" {
		args = append(args, "--session", session)
	}
	args = append(args, message)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "opencode", args...)
	cmd.Dir = ws
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.Stdin = nil
	t0 := time.Now()
	err = cmd.Run()
	inv.WallMS = time.Since(t0).Milliseconds()
	if cctx.Err() == context.DeadlineExceeded {
		inv.TimedOut = true
	}
	if cmd.ProcessState != nil {
		inv.Exit = cmd.ProcessState.ExitCode()
	}
	return inv, nil
}

func oc(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("opencode", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("opencode %s: %v: %s", strings.Join(args, " "), err, errb.String())
	}
	return out.Bytes(), nil
}

// ExportSession writes the session export JSON to path (no model call).
func ExportSession(ws, session, path string) error {
	out, err := oc(ws, "session", "export", session)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// ImportSession imports a continuation into ws (no model call).
func ImportSession(ws, path string) error {
	_, err := oc(ws, "session", "import", path, "--directory", ws)
	return err
}

// SnapshotFiles resolves an OpenCode worktree snapshot (a tree object in
// OpenCode's private snapshot repositories) to path->content. Assay-only:
// it reconstructs the workspace exactly as it was before the first stale
// call, including effects of shell commands that no edit part records.
func SnapshotFiles(tree string) (map[string]string, error) {
	home, _ := os.UserHomeDir()
	roots, _ := filepath.Glob(filepath.Join(home, ".local/share/opencode/snapshot/*/*"))
	for _, gd := range roots {
		cmd := exec.Command("git", "--git-dir", gd, "cat-file", "-t", tree)
		if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "tree" {
			continue
		}
		ls, err := exec.Command("git", "--git-dir", gd, "ls-tree", "-r", "-z", tree).Output()
		if err != nil {
			return nil, err
		}
		files := map[string]string{}
		for _, ent := range strings.Split(string(ls), "\x00") {
			if ent == "" {
				continue
			}
			tab := strings.Index(ent, "\t")
			meta := strings.Fields(ent[:tab])
			if len(meta) < 3 || meta[1] != "blob" {
				continue
			}
			body, err := exec.Command("git", "--git-dir", gd, "cat-file", "blob", meta[2]).Output()
			if err != nil {
				return nil, err
			}
			files[ent[tab+1:]] = string(body)
		}
		return files, nil
	}
	return nil, fmt.Errorf("snapshot tree %s not found", tree)
}

// WriteFiles writes path->content under dir.
func WriteFiles(dir string, files map[string]string) error {
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Rebase computes the workspace for a selective replay: the agent's
// preserved changes (snapshot relative to the old base) applied on top of
// the new base. A path changed both by the agent and by the concurrent
// change is a conflict: the result is UNKNOWN and the caller must fall
// back to a full restart.
func Rebase(oldBase, newBase, snapshot map[string]string) (map[string]string, []string) {
	out := map[string]string{}
	for p, c := range newBase {
		out[p] = c
	}
	var conflicts []string
	seen := map[string]bool{}
	for p, c := range snapshot {
		seen[p] = true
		old, had := oldBase[p]
		if had && old == c {
			continue // untouched by the agent
		}
		if nb, ok := newBase[p]; ok && (!had || nb != old) && nb != c {
			conflicts = append(conflicts, p)
			continue
		}
		out[p] = c
	}
	for p, old := range oldBase {
		if seen[p] {
			continue
		}
		// Agent deleted p.
		if nb, ok := newBase[p]; ok && nb != old {
			conflicts = append(conflicts, p)
			continue
		}
		delete(out, p)
	}
	return out, conflicts
}
