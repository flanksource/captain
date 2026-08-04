// Materialization (§1.3, R1.3/H18): read-tree + checkout-index against an
// absolute destination, never git archive (R6.5/H9), with the non-empty and
// file-count assertions that turn the silent-false-accept trap into a loud
// failure. Exit status 0 from checkout-index is not proof of materialization.
package gitagent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Materialize checks out commitOID's full tree into dst and returns the file
// count. env should be the hook environment so quarantined objects stay
// readable; every path is absolutized before use (R1.3).
func Materialize(ctx context.Context, repoDir string, env []string, commitOID, dst string) (int, error) {
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dstAbs, 0o755); err != nil {
		return 0, err
	}
	names, err := treeEntryNames(ctx, repoDir, env, commitOID)
	if err != nil {
		return 0, err
	}
	if err := RejectDotGitComponents(names); err != nil {
		return 0, err
	}
	idxDir, err := os.MkdirTemp("", "captain-gitagent-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(idxDir)
	ienv := envWith(env, "GIT_INDEX_FILE="+filepath.Join(idxDir, "index"))
	if _, err := runGit(ctx, repoDir, ienv, "read-tree", commitOID); err != nil {
		return 0, err
	}
	// The --work-tree + core.bare=false form is the one verified to write a
	// full tree from a bare receiver during pre-receive (§1.3); --prefix
	// refuses to run without a work tree there.
	if _, err := runGit(ctx, repoDir, ienv,
		"-c", "core.bare=false", "--work-tree="+dstAbs, "checkout-index", "-a", "-f"); err != nil {
		return 0, err
	}
	count, err := countMaterialized(dstAbs)
	if err != nil {
		return 0, err
	}
	if err := AssertMaterialized(count, len(names)); err != nil {
		return 0, fmt.Errorf("%w (destination %s)", err, dstAbs)
	}
	return count, nil
}

func treeEntryNames(ctx context.Context, repoDir string, env []string, commitOID string) ([]string, error) {
	out, err := runGitRaw(ctx, repoDir, env, nil, "ls-tree", "-r", "-z", "--name-only", commitOID)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Split(out, "\x00") {
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// RejectDotGitComponents refuses any path with a .git component (H9 defense
// in depth — receive.fsckObjects already rejects such trees on the wire).
func RejectDotGitComponents(paths []string) error {
	for _, p := range paths {
		for _, component := range strings.Split(p, "/") {
			if strings.EqualFold(component, ".git") {
				return fmt.Errorf("tree contains a .git path component (%q); refusing to materialize (H9)", p)
			}
		}
	}
	return nil
}

// AssertMaterialized is the H18 guard: an empty or short materialization is a
// silent false-accept, not a success.
func AssertMaterialized(got, expected int) error {
	if expected == 0 {
		return fmt.Errorf("refusing to verify an empty tree (H18)")
	}
	if got == 0 {
		return fmt.Errorf("materialization wrote nothing despite checkout-index exiting 0 (H18)")
	}
	if got != expected {
		return fmt.Errorf("materialized %d files but the tree holds %d (H18)", got, expected)
	}
	return nil
}

func countMaterialized(dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}
