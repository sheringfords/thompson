package livepilot

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// ids documents the frozen background-change inventory (count asserted below).

// Every frozen background commit must keep `go test ./...` green: a red
// baseline would confound all oracles. This test pins that property.
func TestBackgroundCommitsStayGreen(t *testing.T) {
	changes := BackgroundChanges()
	names := make([]string, 0, len(changes))
	for n := range changes {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) != 8 {
		t.Fatalf("want 8 frozen background changes, got %d (%v)", len(names), names)
	}
	for _, name := range names {
		ch := changes[name]
		dir := t.TempDir()
		if _, err := Materialize(dir, FixtureAuthorDate); err != nil {
			t.Fatal(err)
		}
		for p, body := range ch.Files {
			if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("go", "test", "./...")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s breaks tests: %v: %s", name, err, out)
		}
	}
}
