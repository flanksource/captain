package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPathFilter_Plans(t *testing.T) {
	plan := "/home/u/.claude/plans/the-plan.md"

	if newPathFilter(false, true).keep(plan) {
		t.Error("plan file should be hidden when includePlans=false")
	}
	if !newPathFilter(true, true).keep(plan) {
		t.Error("plan file should be shown when includePlans=true")
	}
}

func TestPathFilter_Ignored(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.txt\n.tmp/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(repo, "tracked.go")
	ignored := filepath.Join(repo, "ignored.txt")
	for _, p := range []string{tracked, ignored} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "loose.txt") // a different temp dir, no git
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		path   string
		ignore bool
	}{
		{"tracked file", tracked, false},
		{"gitignored file", ignored, true},
		{"out-of-repo file", outside, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := computeIgnored(c.path); got != c.ignore {
				t.Errorf("computeIgnored(%s) = %v, want %v", c.path, got, c.ignore)
			}
			// keep(): hidden when ignored and includeIgnored=false; always shown when true.
			if got := newPathFilter(true, false).keep(c.path); got == c.ignore {
				t.Errorf("keep(includeIgnored=false)(%s) = %v, want %v", c.path, got, !c.ignore)
			}
			if !newPathFilter(true, true).keep(c.path) {
				t.Errorf("keep(includeIgnored=true)(%s) should always be true", c.path)
			}
		})
	}
}
