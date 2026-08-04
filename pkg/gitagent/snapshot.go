// The dispatch snapshot (§6.1): the supervisor's dirty worktree captured as a
// commit parented on HEAD, built from an explicit path set against a
// temporary index — never `read-tree HEAD` + `add --all`, which stages mass
// deletions under sparse-checkout or skip-worktree (R6.1/H4). Blobs are
// hashed with --no-filters so no clean filter, CRLF rule or attribute can
// change the bytes (R6.2); anything that cannot round-trip byte-exact — LFS,
// required filters, dirty submodules — refuses loudly instead (H5).
package gitagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Default snapshot caps, applied when the policy leaves a cap zero.
const (
	DefaultSnapshotMaxFiles     = 5000
	DefaultSnapshotMaxFileSize  = int64(32 << 20)
	DefaultSnapshotMaxTotalSize = int64(256 << 20)
)

const zeroOID = "0000000000000000000000000000000000000000"

// SnapshotPolicy bounds what a dispatch snapshot may carry.
type SnapshotPolicy struct {
	// Paths are doublestar globs; a leading ! denies. With any non-negated
	// pattern present, a path must match one to be included.
	Paths        []string
	MaxFiles     int
	MaxFileSize  int64
	MaxTotalSize int64
}

// Snapshot is the result of TakeSnapshot: a commit whose tree is the dirty
// worktree state and whose parent is the base the supervisor dispatched from.
type Snapshot struct {
	Commit string
	Tree   string
	Base   string
	Paths  []string // repo-relative dirty paths the snapshot applied
}

// TakeSnapshot captures repoDir's dirty worktree as a commit parented on
// HEAD. The worktree, index and repo are left untouched.
func TakeSnapshot(ctx context.Context, repoDir string, policy SnapshotPolicy) (*Snapshot, error) {
	dir, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}
	env := ScrubGitEnv(os.Environ())
	base, err := runGit(ctx, dir, env, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("dispatch requires a repository with at least one commit: %w", err)
	}
	entries, err := statusZ(ctx, dir, env)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Unmerged() {
			return nil, fmt.Errorf("snapshot refused: %q is unmerged; resolve the conflict before dispatching", e.Path)
		}
	}
	all := make([]string, 0, len(entries))
	for _, e := range entries {
		all = append(all, e.Path)
	}
	paths, err := filterPolicyPaths(all, policy.Paths)
	if err != nil {
		return nil, err
	}
	if err := refuseSnapshotHazards(ctx, dir, env, paths); err != nil {
		return nil, err
	}
	if err := enforceSnapshotCaps(dir, paths, policy); err != nil {
		return nil, err
	}
	tree, err := buildSnapshotTree(ctx, dir, env, base, paths)
	if err != nil {
		return nil, err
	}
	commit, err := commitSnapshotTree(ctx, dir, env, tree, base)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Commit: commit, Tree: tree, Base: base, Paths: paths}, nil
}

// filterPolicyPaths applies allow/deny globs. Exclusion is the point of a
// path policy — an excluded dirty path stays at its base version.
func filterPolicyPaths(paths, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return paths, nil
	}
	var allows, denies []string
	for _, p := range patterns {
		pattern, negated := strings.CutPrefix(p, "!")
		if !doublestar.ValidatePattern(pattern) {
			return nil, fmt.Errorf("invalid policy path pattern %q", p)
		}
		if negated {
			denies = append(denies, pattern)
		} else {
			allows = append(allows, pattern)
		}
	}
	matchAny := func(patterns []string, path string) bool {
		for _, p := range patterns {
			if ok, _ := doublestar.Match(p, path); ok {
				return true
			}
		}
		return false
	}
	var out []string
	for _, path := range paths {
		if matchAny(denies, path) {
			continue
		}
		if len(allows) > 0 && !matchAny(allows, path) {
			continue
		}
		out = append(out, path)
	}
	return out, nil
}

// refuseSnapshotHazards aborts on anything that round-trips incorrectly and
// silently (H5): LFS-filtered paths, required clean/smudge filters, and dirty
// submodules.
func refuseSnapshotHazards(ctx context.Context, dir string, env []string, paths []string) error {
	if err := refuseLFS(ctx, dir, env, paths); err != nil {
		return err
	}
	code, out, err := gitExitCode(ctx, dir, env, "config", "--get-regexp", `^filter\..*\.required$`)
	if err != nil {
		return err
	}
	if code == 0 {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.HasSuffix(strings.TrimSpace(line), "true") {
				return fmt.Errorf("snapshot refused: required clean/smudge filter declared (%s); it cannot round-trip byte-exact", strings.Fields(line)[0])
			}
		}
	}
	return refuseDirtySubmodules(ctx, dir, env, paths)
}

// refuseLFS rejects a snapshot when any candidate path resolves the lfs
// filter, or when any in-repo .gitattributes declares it at all — equivalent
// to "`git lfs ls-files` is non-empty" without requiring the lfs binary.
func refuseLFS(ctx context.Context, dir string, env []string, paths []string) error {
	if len(paths) > 0 {
		args := append([]string{"check-attr", "-z", "filter", "--"}, paths...)
		out, err := runGitRaw(ctx, dir, env, nil, args...)
		if err != nil {
			return err
		}
		fields := strings.Split(out, "\x00")
		for i := 0; i+2 < len(fields); i += 3 {
			if fields[i+2] == "lfs" {
				return fmt.Errorf("snapshot refused: %q is LFS-tracked; LFS pointers do not round-trip (H5)", fields[i])
			}
		}
	}
	out, err := runGitRaw(ctx, dir, env, nil,
		"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".gitattributes", ":(glob)**/.gitattributes")
	if err != nil {
		return err
	}
	for _, attrFile := range strings.Split(out, "\x00") {
		if attrFile == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, attrFile))
		if err != nil {
			continue // deleted attributes file cannot declare anything
		}
		if strings.Contains(string(data), "filter=lfs") {
			return fmt.Errorf("snapshot refused: %s declares filter=lfs; dispatch does not support LFS repositories (H5)", attrFile)
		}
	}
	return nil
}

// refuseDirtySubmodules rejects when a dirty path is a gitlink: a submodule's
// content cannot travel in the snapshot, so its dirt would be silently lost.
func refuseDirtySubmodules(ctx context.Context, dir string, env []string, paths []string) error {
	out, err := runGitRaw(ctx, dir, env, nil, "ls-files", "-z", "--stage")
	if err != nil {
		return err
	}
	gitlinks := map[string]bool{}
	for _, record := range strings.Split(out, "\x00") {
		if !strings.HasPrefix(record, "160000 ") {
			continue
		}
		if _, path, found := strings.Cut(record, "\t"); found {
			gitlinks[path] = true
		}
	}
	for _, p := range paths {
		if gitlinks[p] {
			return fmt.Errorf("snapshot refused: submodule %q is dirty; commit or clean it before dispatching (H5)", p)
		}
	}
	return nil
}

func enforceSnapshotCaps(dir string, paths []string, policy SnapshotPolicy) error {
	maxFiles := policy.MaxFiles
	if maxFiles == 0 {
		maxFiles = DefaultSnapshotMaxFiles
	}
	maxFile := policy.MaxFileSize
	if maxFile == 0 {
		maxFile = DefaultSnapshotMaxFileSize
	}
	maxTotal := policy.MaxTotalSize
	if maxTotal == 0 {
		maxTotal = DefaultSnapshotMaxTotalSize
	}
	if len(paths) > maxFiles {
		return fmt.Errorf("snapshot refused: %d dirty paths exceed the %d-file cap", len(paths), maxFiles)
	}
	var total int64
	for _, p := range paths {
		fi, err := os.Lstat(filepath.Join(dir, p))
		if err != nil {
			continue // a deletion has no size
		}
		if fi.Mode().IsRegular() && fi.Size() > maxFile {
			return fmt.Errorf("snapshot refused: %q is %d bytes, over the %d-byte cap", p, fi.Size(), maxFile)
		}
		total += fi.Size()
		if total > maxTotal {
			return fmt.Errorf("snapshot refused: dirty state exceeds the %d-byte total cap", maxTotal)
		}
	}
	return nil
}

// buildSnapshotTree stages exactly paths over base in a throwaway index and
// returns the written tree.
func buildSnapshotTree(ctx context.Context, dir string, env []string, base string, paths []string) (string, error) {
	idxDir, err := os.MkdirTemp("", "captain-gitagent-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(idxDir)
	ienv := envWith(env, "GIT_INDEX_FILE="+filepath.Join(idxDir, "index"))
	if _, err := runGit(ctx, dir, ienv, "read-tree", base); err != nil {
		return "", err
	}
	var info bytes.Buffer
	for _, p := range paths {
		line, err := indexEntry(ctx, dir, env, p)
		if err != nil {
			return "", err
		}
		info.WriteString(line)
		info.WriteByte(0)
	}
	if info.Len() > 0 {
		if _, err := runGitIn(ctx, dir, ienv, &info, "update-index", "-z", "--index-info"); err != nil {
			return "", err
		}
	}
	return runGit(ctx, dir, ienv, "write-tree")
}

// indexEntry renders one `update-index --index-info` record for the path's
// on-disk state: mode 0 removes a deleted path, symlinks hash their target,
// and regular files are hashed with --no-filters so the blob is byte-exact.
func indexEntry(ctx context.Context, dir string, env []string, path string) (string, error) {
	full := filepath.Join(dir, path)
	fi, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return "0 " + zeroOID + "\t" + path, nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		oid, err := runGitIn(ctx, dir, env, strings.NewReader(target), "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return "", err
		}
		return "120000 " + oid + "\t" + path, nil
	case fi.Mode().IsRegular():
		f, err := os.Open(full)
		if err != nil {
			return "", err
		}
		defer f.Close()
		oid, err := runGitIn(ctx, dir, env, f, "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return "", err
		}
		mode := "100644"
		if fi.Mode()&0o111 != 0 {
			mode = "100755"
		}
		return mode + " " + oid + "\t" + path, nil
	default:
		return "", fmt.Errorf("snapshot refused: %q is a %s, which git cannot carry", path, fi.Mode().Type())
	}
}

func commitSnapshotTree(ctx context.Context, dir string, env []string, tree, base string) (string, error) {
	cenv := envWith(env,
		"GIT_AUTHOR_NAME=captain",
		"GIT_AUTHOR_EMAIL=captain@localhost",
		"GIT_COMMITTER_NAME=captain",
		"GIT_COMMITTER_EMAIL=captain@localhost",
	)
	return runGitIn(ctx, dir, cenv,
		strings.NewReader("captain dispatch snapshot\n"),
		"commit-tree", tree, "-p", base)
}
