package livepilot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Fixture determinism: two independent materializations must yield identical
// commit hashes, blob OIDs, and passing tests (frozen oracle sanity).
func TestFixtureDeterminism(t *testing.T) {
	mk := func() (string, string) {
		dir := t.TempDir()
		head, err := Materialize(dir, FixtureAuthorDate)
		if err != nil {
			t.Fatal(err)
		}
		return dir, head
	}
	d1, h1 := mk()
	d2, h2 := mk()
	if h1 != h2 {
		t.Fatalf("fixture heads differ: %s vs %s", h1, h2)
	}
	blob := func(dir, path string) string {
		cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD:"+path)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	for _, p := range SortedPaths() {
		if blob(d1, p) != blob(d2, p) {
			t.Fatalf("blob differs for %s", p)
		}
	}
	// Oracle sanity: the fixture's own tests pass.
	for _, dir := range []string{d1, d2} {
		cmd := exec.Command("go", "test", "./...")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture tests fail: %v: %s", err, out)
		}
	}
	// Background commits are deterministic too.
	apply := func(dir, changeID string) string {
		ch := BackgroundChanges()[changeID]
		for p, body := range ch.Files {
			full := filepath.Join(dir, p)
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("git", "-C", dir, "add", "-A")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		cmd = exec.Command("git", "-C", dir, "commit", "-qm", ch.Message)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+FixtureAuthorDate,
			"GIT_COMMITTER_DATE="+FixtureAuthorDate,
			"GIT_AUTHOR_NAME=assay",
			"GIT_AUTHOR_EMAIL=assay@test",
			"GIT_COMMITTER_NAME=assay",
			"GIT_COMMITTER_EMAIL=assay@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		cmd = exec.Command("git", "-C", dir, "rev-parse", "HEAD")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if apply(d1, "local1-premise") != apply(d2, "local1-premise") {
		t.Fatal("background commit hashes differ")
	}
}
