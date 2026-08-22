package commit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky/exec"
)

// git runs `git <args...>` in dir and returns trimmed stdout, failing loud on a
// non-zero exit with stderr context. Every git call in this package goes through
// it so no failure can be silently swallowed into an empty string.
func git(dir string, args ...string) (string, error) {
	res := exec.NewExec("git", args...).WithCwd(dir).Run().Result()
	if res.Error != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), res.Error, strings.TrimSpace(res.Stderr))
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git %s: exit %d: %s", strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

// gitRoot resolves the working tree dir belongs to. Every path this package
// handles is relative to that root — `git status` reports repo-relative paths
// whatever directory it is invoked from — so a run launched inside a
// subdirectory has to be lifted to the root before its paths mean anything.
func gitRoot(dir string) (string, error) {
	root, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("commit: %s is not inside a git working tree: %w", dir, err)
	}
	return canonicalDir(root), nil
}

// canonicalDir renders dir absolute with its symlinks resolved, so two bases
// arrived at by different routes — git's own output versus the caller's cwd —
// compare and join equal. On macOS this is what keeps /tmp and /private/tmp
// from looking like different trees.
func canonicalDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

// dirtyPaths lists every repo-relative path with uncommitted work in dir, in
// git's own order. Ignored files are absent — `git status` excludes them — which
// is what keeps build artifacts out of the chain regardless of gate level.
//
// The -z porcelain format is used rather than the human one because it emits
// paths verbatim: the default format quotes and escapes any path with a space or
// a non-ASCII byte, which a line-based parse then has to unescape by hand.
func dirtyPaths(dir string) ([]string, error) {
	res := exec.NewExec("git", "status", "--porcelain", "-z", "--untracked-files=all").WithCwd(dir).Run().Result()
	if res.Error != nil {
		return nil, fmt.Errorf("git status: %w: %s", res.Error, strings.TrimSpace(res.Stderr))
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("git status: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return parseStatusZ(res.Stdout), nil
}

// parseStatusZ splits `git status --porcelain -z` output into paths. Each record
// is "XY <path>", and a rename/copy is followed by a second record holding the
// source path — which is why this cannot be a plain split.
func parseStatusZ(out string) []string {
	fields := strings.Split(out, "\x00")
	var paths []string
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if len(record) < 4 {
			continue
		}
		x, y := record[0], record[1]
		paths = append(paths, record[3:])
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			// The next field is the rename source; it also needs staging so the
			// commit records a rename rather than an unexplained addition.
			if i+1 < len(fields) && fields[i+1] != "" {
				i++
				paths = append(paths, fields[i])
			}
		}
	}
	return paths
}

// ignoredPaths returns the subset of relPaths git ignores in dir, asking git
// itself rather than reimplementing its pattern language — so .gitignore files at
// any depth, .git/info/exclude and core.excludesFile are all honoured, and a
// linked worktree resolves the same way git does.
//
// --no-index widens the answer to tracked files that also match an ignore rule,
// and that is deliberate: `git add` refuses such a path outright ("The following
// paths are ignored by one of your .gitignore files ... use -f"), tracked or not,
// so a force-added build bundle the agent rebuilt is dirty in `git status` yet
// impossible to stage. Without the flag it would be treated as attributable and
// the whole run would die inside stage() rather than committing what it could.
//
// Every path must be inside dir: git fails the whole invocation with exit 128 on
// an out-of-tree path, so callers filter those out first.
//
// The -z --stdin form is used for the same reason as in dirtyPaths — paths with
// spaces round-trip verbatim instead of arriving quoted and escaped.
func ignoredPaths(dir string, relPaths []string) (map[string]struct{}, error) {
	ignored := make(map[string]struct{}, len(relPaths))
	if len(relPaths) == 0 {
		return ignored, nil
	}
	res := exec.NewExec("git", "check-ignore", "--no-index", "-z", "--stdin").
		WithCwd(dir).
		WithStdin(strings.NewReader(strings.Join(relPaths, "\x00"))).
		Run().Result()
	// Exit 1 is `git check-ignore` reporting that nothing matched — an answer, not
	// a failure. Only a higher code (128, fatal) is a real error, and res.Error is
	// consulted after the code because clicky reports every non-zero exit as one.
	if res.ExitCode == 1 {
		return ignored, nil
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("git check-ignore in %s: exit %d: %s", dir, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	if res.Error != nil {
		return nil, fmt.Errorf("git check-ignore in %s: %w: %s", dir, res.Error, strings.TrimSpace(res.Stderr))
	}
	for _, p := range strings.Split(res.Stdout, "\x00") {
		if p != "" {
			ignored[p] = struct{}{}
		}
	}
	return ignored, nil
}

// stage adds exactly the named paths. The pathspecs are what bound the commit —
// unlike a bare `git add --all` this cannot pick up a file the policy did not
// select.
//
// Deleted files are why this is not a single call: `git add` refuses a pathspec
// that matches nothing on disk, so a path the agent removed has to be staged
// through its index entry with --all. A path that is in neither place was
// already staged as a deletion (the agent ran `git rm` itself) and needs no
// action — adding it would only fail on the missing file.
func stage(dir string, paths []string) error {
	var present, missing []string
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			present = append(present, p)
		} else {
			missing = append(missing, p)
		}
	}
	if len(present) > 0 {
		if _, err := git(dir, append([]string{"add", "--"}, present...)...); err != nil {
			return err
		}
	}
	if len(missing) == 0 {
		return nil
	}
	tracked, err := trackedSubset(dir, missing)
	if err != nil || len(tracked) == 0 {
		return err
	}
	_, err = git(dir, append([]string{"add", "--all", "--"}, tracked...)...)
	return err
}

// trackedSubset returns the paths git still holds an index entry for. A path
// whose deletion is already staged has none, which is how stage tells "delete
// this" apart from "already deleted".
func trackedSubset(dir string, paths []string) ([]string, error) {
	out, err := git(dir, append([]string{"ls-files", "--"}, paths...)...)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// hasStaged reports whether the index holds anything to commit. `git diff
// --cached --quiet` exits 1 when it does, so the exit code is inspected before
// res.Error — clicky reports every non-zero exit as an error, and here 1 is the
// answer rather than a failure.
func hasStaged(dir string) (bool, error) {
	res := exec.NewExec("git", "diff", "--cached", "--quiet").WithCwd(dir).Run().Result()
	switch res.ExitCode {
	case 0:
		return false, nil
	case 1:
		return true, nil
	}
	if res.Error != nil {
		return false, fmt.Errorf("git diff --cached: %w: %s", res.Error, strings.TrimSpace(res.Stderr))
	}
	return false, fmt.Errorf("git diff --cached: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
}

// commitStaged commits the index with subject and returns the new SHA.
func commitStaged(dir, subject string) (string, error) {
	if _, err := git(dir, "commit", "--no-verify", "-m", subject); err != nil {
		return "", err
	}
	return git(dir, "rev-parse", "HEAD")
}

// commitFixup commits the index as a `fixup!` of anchor, so the chain collapses
// back onto it at autosquash time. No message is composed — that is the point:
// a fixup borrows the anchor's subject, so committing a turn never waits on (or
// fails because of) a model call.
func commitFixup(dir, anchor string) (string, error) {
	if _, err := git(dir, "commit", "--no-verify", "--fixup="+anchor); err != nil {
		return "", err
	}
	return git(dir, "rev-parse", "HEAD")
}

// commitAmend folds the index into HEAD, keeping its message.
func commitAmend(dir string) (string, error) {
	if _, err := git(dir, "commit", "--no-verify", "--amend", "--no-edit"); err != nil {
		return "", err
	}
	return git(dir, "rev-parse", "HEAD")
}

// resolveRef verifies a ref exists and returns the commit it names.
func resolveRef(dir, ref string) (string, error) {
	sha, err := git(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		return "", fmt.Errorf("commit: cannot resolve ref %q in %s", ref, dir)
	}
	return sha, nil
}

// autosquashBase is the ref to rebase onto so the chain — and only the chain —
// is replayed. The anchor's parent spans exactly the run's own commits; when the
// anchor is a root commit there is no parent and the rebase runs with --root.
func autosquashBase(dir, anchor string) (base string, root bool, err error) {
	sha, err := git(dir, "rev-parse", "--verify", "--quiet", anchor+"^^{commit}")
	if err != nil || sha == "" {
		// No parent: the anchor is the repo's first commit.
		return "", true, nil
	}
	return sha, false, nil
}

// autosquash collapses the fixup chain with a non-interactive
// `git rebase -i --autosquash`, forcing both editors to a no-op so git accepts
// the generated todo list unattended. On conflict the rebase is aborted so the
// tree is never handed back mid-rebase, and the failure is reported rather than
// silently leaving a `fixup!` chain behind.
func autosquash(dir, base string, root bool) error {
	args := []string{"-c", "sequence.editor=:", "rebase", "-i", "--autosquash"}
	if root {
		args = append(args, "--root")
	} else {
		args = append(args, base)
	}
	res := exec.NewExec("git", args...).
		WithCwd(dir).
		WithEnv(map[string]string{"GIT_SEQUENCE_EDITOR": ":", "GIT_EDITOR": ":"}).
		Run().Result()
	if res.Error == nil && res.ExitCode == 0 {
		return nil
	}
	target := base
	if root {
		target = "--root"
	}
	stderr := strings.TrimSpace(res.Stderr)
	if strings.Contains(stderr, "CONFLICT") || strings.Contains(stderr, "could not apply") {
		if _, abortErr := git(dir, "rebase", "--abort"); abortErr != nil {
			return fmt.Errorf("autosquash onto %s conflicted and could not be aborted: %w", target, abortErr)
		}
		return fmt.Errorf("autosquash onto %s conflicted; the rebase was aborted and the fixup chain left intact — squash it manually or set squash: false", target)
	}
	if res.Error != nil {
		return fmt.Errorf("git rebase --autosquash %s: %w: %s", target, res.Error, stderr)
	}
	return fmt.Errorf("git rebase --autosquash %s: exit %d: %s", target, res.ExitCode, stderr)
}
