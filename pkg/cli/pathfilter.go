package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/claude/tools"
)

// pathFilter decides whether a written file path should be surfaced in `changes`
// / `transcript`, applying the --plans and --ignored flags. Gitignore lookups are
// cached per path since the same file can appear across many tool uses.
type pathFilter struct {
	includePlans   bool
	includeIgnored bool
	ignoredCache   map[string]bool
}

func newPathFilter(includePlans, includeIgnored bool) *pathFilter {
	return &pathFilter{
		includePlans:   includePlans,
		includeIgnored: includeIgnored,
		ignoredCache:   map[string]bool{},
	}
}

// keep reports whether path should be shown. Plan files (~/.claude/plans) are
// hidden unless includePlans; gitignored or out-of-repo files are hidden unless
// includeIgnored.
func (f *pathFilter) keep(path string) bool {
	if path == "" {
		return true
	}
	// changes/transcript paths are relativized to the project root; absolutize
	// (against the working dir) so plan-path and gitignore checks are reliable.
	abs := path
	if !filepath.IsAbs(abs) {
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
	}
	if !f.includePlans && strings.Contains(filepath.ToSlash(abs), "/.claude/plans/") {
		return false
	}
	if !f.includeIgnored && f.isIgnored(abs) {
		return false
	}
	return true
}

// filterToolsByPath drops Edit/Write tool rows whose written file(s) are all
// hidden by the plan/ignore flags. Rows with no write paths (Bash, Read, …) are kept.
func filterToolsByPath(tl []tools.Tool, pf *pathFilter) []tools.Tool {
	out := make([]tools.Tool, 0, len(tl))
	for _, t := range tl {
		writes := AnalyzeToolUse(t).WritePaths
		keep := len(writes) == 0
		for _, p := range writes {
			if pf.keep(p) {
				keep = true
				break
			}
		}
		if keep {
			out = append(out, t)
		}
	}
	return out
}

func (f *pathFilter) isIgnored(path string) bool {
	if v, ok := f.ignoredCache[path]; ok {
		return v
	}
	v := computeIgnored(path)
	f.ignoredCache[path] = v
	return v
}

// computeIgnored treats a path as ignored when it lives outside any git repo, or
// when `git check-ignore` matches it inside its repo.
func computeIgnored(path string) bool {
	repo := repoRoot(path)
	if repo == "" {
		return true
	}
	err := exec.Command("git", "-C", repo, "check-ignore", "-q", path).Run()
	if err == nil {
		return true // exit 0 → ignored
	}
	// exit 1 → not ignored; any other error → don't over-filter.
	return false
}

// repoRoot walks up from path to the enclosing git repo root (dir containing a
// .git entry), or "" if the path is not inside a repository.
func repoRoot(path string) string {
	dir := path
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		dir = filepath.Dir(path)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
