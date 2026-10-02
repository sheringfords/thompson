// Package realreplay implements the real-replay economics assay
// (THOMPSON_EVIDENCE_CLOSEOUT_AND_REAL_REPLAY_ECONOMICS_V1): an agent run is
// executed against versioned external state, the state changes
// semantically, and recovery is executed for real, either as a full
// restart or as an actual selective replay that resumes a truncated,
// observable-only transcript. Research harness only: no product, no server
// beyond the local HTTP fixture, no scheduler.
//
// Authority rule: Git (refs, commits, trees, blobs) and the HTTP fixture
// (strong ETags) are the only version authorities. This package stores
// premise provenance (path/URL -> native witness), never a version of its
// own.
package realreplay

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Deterministic commit identity so fixture OIDs are reproducible.
var gitEnv = []string{
	"GIT_AUTHOR_NAME=assay", "GIT_AUTHOR_EMAIL=assay@example.invalid",
	"GIT_COMMITTER_NAME=assay", "GIT_COMMITTER_EMAIL=assay@example.invalid",
	"GIT_AUTHOR_DATE=2026-10-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-10-01T00:00:00Z",
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
}

// Git runs git with the deterministic identity in dir.
func Git(dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, errb.String())
	}
	return out.String(), nil
}

// Authority is a bare repository whose refs/heads/main is the sole
// authority for repository state.
type Authority struct{ Dir string }

// NewAuthority creates a bare repo whose main points at a commit holding
// files (parent-less).
func NewAuthority(dir string, files map[string]string) (*Authority, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	if _, err := Git(dir, nil, "init", "-q", "--bare", "-b", "main"); err != nil {
		return nil, "", err
	}
	a := &Authority{Dir: dir}
	tree, err := a.writeTree(files)
	if err != nil {
		return nil, "", err
	}
	c, err := a.commitTree(tree, "", "C0 fixture")
	if err != nil {
		return nil, "", err
	}
	if _, err := Git(dir, nil, "update-ref", "refs/heads/main", c); err != nil {
		return nil, "", err
	}
	return a, c, nil
}

// Clone copies the authority (all objects and refs) to dir, for running
// independent treatments from identical history.
func (a *Authority) Clone(dir string) (*Authority, error) {
	if _, err := Git(filepath.Dir(dir), nil, "clone", "-q", "--bare", a.Dir, dir); err != nil {
		return nil, err
	}
	return &Authority{Dir: dir}, nil
}

// Main returns the current main commit OID.
func (a *Authority) Main() (string, error) {
	out, err := Git(a.Dir, nil, "rev-parse", "refs/heads/main")
	return strings.TrimSpace(out), err
}

func (a *Authority) writeTree(files map[string]string) (string, error) {
	idx := filepath.Join(a.Dir, "tmp-index")
	defer os.Remove(idx)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		oid, err := Git(a.Dir, []byte(files[p]), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		cmd := exec.Command("git", "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(oid)+","+p)
		cmd.Dir = a.Dir
		cmd.Env = append(append(os.Environ(), gitEnv...), "GIT_INDEX_FILE="+idx)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("update-index %s: %v: %s", p, err, out)
		}
	}
	cmd := exec.Command("git", "write-tree")
	cmd.Dir = a.Dir
	cmd.Env = append(append(os.Environ(), gitEnv...), "GIT_INDEX_FILE="+idx)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func (a *Authority) commitTree(tree, parent, msg string) (string, error) {
	args := []string{"commit-tree", tree, "-m", msg}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	out, err := Git(a.Dir, nil, args...)
	return strings.TrimSpace(out), err
}

// Files returns path->content of a commit's tree.
func (a *Authority) Files(commit string) (map[string]string, error) {
	out, err := Git(a.Dir, nil, "ls-tree", "-r", "--name-only", commit)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, p := range strings.Split(strings.TrimSpace(out), "\n") {
		if p == "" {
			continue
		}
		body, err := Git(a.Dir, nil, "show", commit+":"+p)
		if err != nil {
			return nil, err
		}
		files[p] = body
	}
	return files, nil
}

// ConcurrentCommit lands a commit by another committer on main: the
// files in change replace (or add) paths on top of the current main. It is
// the injected semantic state change; it never touches agent workspaces.
func (a *Authority) ConcurrentCommit(change map[string]string, msg string) (string, error) {
	head, err := a.Main()
	if err != nil {
		return "", err
	}
	files, err := a.Files(head)
	if err != nil {
		return "", err
	}
	for p, c := range change {
		files[p] = c
	}
	tree, err := a.writeTree(files)
	if err != nil {
		return "", err
	}
	c, err := a.commitTree(tree, head, msg)
	if err != nil {
		return "", err
	}
	if _, err := Git(a.Dir, nil, "update-ref", "refs/heads/main", c, head); err != nil {
		return "", err
	}
	return c, nil
}

// Witness returns the native witness at commit for path: the blob OID for
// a file, the tree OID for a directory ("" or "." is the root tree), or
// "absent".
func (a *Authority) Witness(commit, path string) (string, error) {
	p := strings.Trim(filepath.ToSlash(path), "/")
	spec := commit + "^{tree}"
	if p != "" && p != "." {
		spec = commit + ":" + p
	}
	out, err := Git(a.Dir, nil, "rev-parse", "--verify", "--quiet", spec)
	if err != nil {
		return "absent", nil
	}
	return strings.TrimSpace(out), nil
}

// CommitWorkspace records the workspace's file content (excluding .git)
// as a commit whose parent is expected, then moves main from expected to it
// with a compare-and-swap (git update-ref <new> <old>). If main moved since
// validation the CAS fails and nothing is committed.
func (a *Authority) CommitWorkspace(ws, expected, msg string) (string, error) {
	files, err := ReadWorkspace(ws)
	if err != nil {
		return "", err
	}
	tree, err := a.writeTree(files)
	if err != nil {
		return "", err
	}
	c, err := a.commitTree(tree, expected, msg)
	if err != nil {
		return "", err
	}
	if _, err := Git(a.Dir, nil, "update-ref", "refs/heads/main", c, expected); err != nil {
		return "", fmt.Errorf("conditional commit rejected (main moved): %w", err)
	}
	return c, nil
}

// ReadWorkspace returns path->content for every regular file under ws,
// excluding .git and the per-run opencode config.
func ReadWorkspace(ws string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.Walk(ws, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = string(b)
		return nil
	})
	return files, err
}

// Materialize replaces ws content with a non-bare clone of the authority
// checked out (detached) at commit. The directory path is preserved so
// treatments share an identical working-directory string.
func (a *Authority) Materialize(ws, commit string) error {
	if err := ClearDir(ws); err != nil {
		return err
	}
	if _, err := Git(filepath.Dir(ws), nil, "clone", "-q", "--no-checkout", a.Dir, ws); err != nil {
		return err
	}
	_, err := Git(ws, nil, "checkout", "-q", "--detach", commit)
	return err
}

// ClearDir removes everything inside dir (creating it if needed).
func ClearDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// NamesWitness is a projection of native tree objects: a hash of the
// sorted entry names (and types) under dir at commit, recursive or not. It
// is recomputed from the authority on every validation, so it is derived
// from the native witness and never stored as an independent version. It
// changes when entries are added, removed or renamed, not when file
// content changes, matching what a directory listing exposes.
func (a *Authority) NamesWitness(commit, dir string, recursive bool) (string, error) {
	d := strings.Trim(filepath.ToSlash(dir), "/")
	args := []string{"ls-tree", "--full-tree"}
	if recursive {
		args = append(args, "-r", "-t")
	}
	spec := commit
	if d != "" && d != "." {
		args = append(args, commit, d+"/")
	} else {
		args = append(args, spec)
	}
	out, err := Git(a.Dir, nil, args...)
	if err != nil {
		return "absent", nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 2)
		meta := strings.Fields(f[0])
		if len(f) == 2 && len(meta) >= 2 {
			names = append(names, meta[1]+" "+f[1])
		}
	}
	if len(names) == 0 {
		return "absent", nil
	}
	sort.Strings(names)
	return "names:" + ETagOf(strings.Join(names, "\n"))[1:17], nil
}

// IsTree reports whether path names a directory at commit.
func (a *Authority) IsTree(commit, path string) bool {
	p := strings.Trim(filepath.ToSlash(path), "/")
	if p == "" || p == "." {
		return true
	}
	out, err := Git(a.Dir, nil, "cat-file", "-t", commit+":"+p)
	return err == nil && strings.TrimSpace(out) == "tree"
}
