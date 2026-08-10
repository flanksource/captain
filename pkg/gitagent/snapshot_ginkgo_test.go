package gitagent_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
)

// gitT runs git in dir with a pinned identity, failing the spec on error.
func gitT(dir string, args ...string) string {
	GinkgoHelper()
	full := append([]string{
		"-c", "user.name=test", "-c", "user.email=test@localhost",
		"-c", "init.defaultBranch=main", "-c", "protocol.file.allow=always",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %v:\n%s", args, out)
	return strings.TrimSpace(string(out))
}

func writeFileT(dir, path, content string) {
	GinkgoHelper()
	full := filepath.Join(dir, path)
	Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
	Expect(os.WriteFile(full, []byte(content), 0o644)).To(Succeed())
}

// blob returns the byte content of path in the given tree-ish, or "" if absent.
func blob(dir, treeish, path string) string {
	cmd := exec.Command("git", "cat-file", "blob", treeish+":"+path)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// treeMode returns the mode of path in the tree-ish, or "" if absent.
func treeMode(dir, treeish, path string) string {
	cmd := exec.Command("git", "ls-tree", treeish, "--", path)
	cmd.Dir = dir
	out, _ := cmd.Output()
	fields := strings.Fields(string(out))
	if len(fields) < 1 {
		return ""
	}
	return fields[0]
}

// newFidelityRepo builds the A4.3 fixture: a repo whose dirty state covers
// modifications, staged and unstaged deletions, a rename, an exec-bit flip, a
// symlink, nested and odd-named untracked files, and CRLF bytes under a text
// attribute plus core.autocrlf=true.
func newFidelityRepo() string {
	dir := GinkgoT().TempDir()
	gitT(dir, "init", "-q")
	writeFileT(dir, "keep.txt", "keep\n")
	writeFileT(dir, "mod.txt", "v1\n")
	writeFileT(dir, "del-staged.txt", "doomed\n")
	writeFileT(dir, "del-unstaged.txt", "doomed too\n")
	writeFileT(dir, "ren-old.txt", "rename me\n")
	writeFileT(dir, "exec.sh", "#!/bin/sh\n")
	writeFileT(dir, "sub/nested.txt", "nested\n")
	writeFileT(dir, "crlf.txt", "a\nb\n")
	gitT(dir, "add", "-A")
	gitT(dir, "commit", "-q", "-m", "base")

	writeFileT(dir, "mod.txt", "v2\n")
	gitT(dir, "rm", "-q", "del-staged.txt")
	Expect(os.Remove(filepath.Join(dir, "del-unstaged.txt"))).To(Succeed())
	gitT(dir, "mv", "ren-old.txt", "ren-new.txt")
	writeFileT(dir, "ren-new.txt", "renamed v2\n")
	Expect(os.Chmod(filepath.Join(dir, "exec.sh"), 0o755)).To(Succeed())
	Expect(os.Symlink("mod.txt", filepath.Join(dir, "link.txt"))).To(Succeed())
	writeFileT(dir, "newdir/new.txt", "brand new\n")
	writeFileT(dir, "with space.txt", "spaced\n")
	// CRLF bytes on disk, with every conversion knob armed against them.
	Expect(os.WriteFile(filepath.Join(dir, "crlf.txt"), []byte("a\r\nb\r\n"), 0o644)).To(Succeed())
	writeFileT(dir, ".gitattributes", "*.txt text\n")
	gitT(dir, "config", "core.autocrlf", "true")
	return dir
}

var _ = Describe("dispatch snapshot", func() {
	ctx := context.Background()

	It("captures the dirty worktree byte-exactly and leaves the repo untouched", func() {
		dir := newFidelityRepo()
		head := gitT(dir, "rev-parse", "HEAD")
		statusBefore := gitT(dir, "status", "--porcelain")

		snap, err := gitagent.TakeSnapshot(ctx, dir, gitagent.SnapshotPolicy{})
		Expect(err).NotTo(HaveOccurred())
		Expect(snap.Base).To(Equal(head))
		Expect(gitT(dir, "rev-parse", snap.Commit+"^")).To(Equal(head))
		Expect(gitT(dir, "rev-parse", snap.Commit+"^{tree}")).To(Equal(snap.Tree))

		Expect(blob(dir, snap.Commit, "mod.txt")).To(Equal("v2\n"))
		Expect(treeMode(dir, snap.Commit, "del-staged.txt")).To(BeEmpty())
		Expect(treeMode(dir, snap.Commit, "del-unstaged.txt")).To(BeEmpty())
		Expect(treeMode(dir, snap.Commit, "ren-old.txt")).To(BeEmpty())
		Expect(blob(dir, snap.Commit, "ren-new.txt")).To(Equal("renamed v2\n"))
		Expect(treeMode(dir, snap.Commit, "exec.sh")).To(Equal("100755"))
		Expect(treeMode(dir, snap.Commit, "link.txt")).To(Equal("120000"))
		Expect(blob(dir, snap.Commit, "link.txt")).To(Equal("mod.txt"))
		Expect(blob(dir, snap.Commit, "newdir/new.txt")).To(Equal("brand new\n"))
		Expect(blob(dir, snap.Commit, "with space.txt")).To(Equal("spaced\n"))
		// R6.2: the CRLF bytes on disk are the bytes in the snapshot, despite
		// core.autocrlf=true and the `*.txt text` attribute.
		Expect(blob(dir, snap.Commit, "crlf.txt")).To(Equal("a\r\nb\r\n"))
		// An untouched file keeps its base blob.
		Expect(blob(dir, snap.Commit, "keep.txt")).To(Equal("keep\n"))

		// The snapshot must not disturb the repo: same HEAD, same dirt.
		Expect(gitT(dir, "rev-parse", "HEAD")).To(Equal(head))
		Expect(gitT(dir, "status", "--porcelain")).To(Equal(statusBefore))
	})

	It("does not stage deletions under sparse-checkout (R6.1/H4)", func() {
		dir := GinkgoT().TempDir()
		gitT(dir, "init", "-q")
		writeFileT(dir, "top.txt", "top\n")
		writeFileT(dir, "vendor/dep.txt", "vendored\n")
		gitT(dir, "add", "-A")
		gitT(dir, "commit", "-q", "-m", "base")
		vendorBlob := gitT(dir, "rev-parse", "HEAD:vendor/dep.txt")
		gitT(dir, "sparse-checkout", "set", "--no-cone", "/*", "!vendor/")
		Expect(filepath.Join(dir, "vendor", "dep.txt")).NotTo(BeAnExistingFile())

		writeFileT(dir, "top.txt", "top v2\n")
		snap, err := gitagent.TakeSnapshot(ctx, dir, gitagent.SnapshotPolicy{})
		Expect(err).NotTo(HaveOccurred())
		Expect(blob(dir, snap.Commit, "top.txt")).To(Equal("top v2\n"))
		Expect(gitT(dir, "rev-parse", snap.Commit+":vendor/dep.txt")).To(Equal(vendorBlob))
	})

	It("snapshots a clean worktree as the base tree", func() {
		dir := GinkgoT().TempDir()
		gitT(dir, "init", "-q")
		writeFileT(dir, "a.txt", "a\n")
		gitT(dir, "add", "-A")
		gitT(dir, "commit", "-q", "-m", "base")
		snap, err := gitagent.TakeSnapshot(ctx, dir, gitagent.SnapshotPolicy{})
		Expect(err).NotTo(HaveOccurred())
		Expect(snap.Paths).To(BeEmpty())
		Expect(snap.Tree).To(Equal(gitT(dir, "rev-parse", "HEAD^{tree}")))
	})

	It("applies policy path globs, denies winning over allows", func() {
		dir := GinkgoT().TempDir()
		gitT(dir, "init", "-q")
		writeFileT(dir, "seed.txt", "seed\n")
		gitT(dir, "add", "-A")
		gitT(dir, "commit", "-q", "-m", "base")
		writeFileT(dir, "pkg/a.go", "package a\n")
		writeFileT(dir, "pkg/key.pem", "SECRET\n")
		writeFileT(dir, "docs/readme.md", "docs\n")

		snap, err := gitagent.TakeSnapshot(ctx, dir, gitagent.SnapshotPolicy{
			Paths: []string{"pkg/**", "!**/*.pem"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(snap.Paths).To(ConsistOf("pkg/a.go"))
		Expect(blob(dir, snap.Commit, "pkg/a.go")).To(Equal("package a\n"))
		Expect(treeMode(dir, snap.Commit, "pkg/key.pem")).To(BeEmpty())
		Expect(treeMode(dir, snap.Commit, "docs/readme.md")).To(BeEmpty())
	})

	It("refuses LFS, required filters, dirty submodules and unmerged paths (H5)", func() {
		lfs := GinkgoT().TempDir()
		gitT(lfs, "init", "-q")
		writeFileT(lfs, "seed.txt", "seed\n")
		gitT(lfs, "add", "-A")
		gitT(lfs, "commit", "-q", "-m", "base")
		writeFileT(lfs, ".gitattributes", "*.bin filter=lfs diff=lfs merge=lfs -text\n")
		writeFileT(lfs, "data.bin", "not really lfs\n")
		_, err := gitagent.TakeSnapshot(ctx, lfs, gitagent.SnapshotPolicy{})
		Expect(err).To(MatchError(ContainSubstring("LFS")))

		reqf := GinkgoT().TempDir()
		gitT(reqf, "init", "-q")
		writeFileT(reqf, "seed.txt", "seed\n")
		gitT(reqf, "add", "-A")
		gitT(reqf, "commit", "-q", "-m", "base")
		gitT(reqf, "config", "filter.crypt.required", "true")
		gitT(reqf, "config", "filter.crypt.clean", "cat")
		gitT(reqf, "config", "filter.crypt.smudge", "cat")
		writeFileT(reqf, "dirty.txt", "dirty\n")
		// The bare declaration must not refuse: git-lfs installs one
		// machine-wide, so refusing on it would refuse every repository.
		_, err = gitagent.TakeSnapshot(ctx, reqf, gitagent.SnapshotPolicy{})
		Expect(err).NotTo(HaveOccurred())
		writeFileT(reqf, ".gitattributes", "*.txt filter=crypt\n")
		_, err = gitagent.TakeSnapshot(ctx, reqf, gitagent.SnapshotPolicy{})
		Expect(err).To(MatchError(ContainSubstring("required clean/smudge filter")))

		sub := GinkgoT().TempDir()
		gitT(sub, "init", "-q")
		writeFileT(sub, "inner.txt", "inner\n")
		gitT(sub, "add", "-A")
		gitT(sub, "commit", "-q", "-m", "sub base")
		super := GinkgoT().TempDir()
		gitT(super, "init", "-q")
		writeFileT(super, "seed.txt", "seed\n")
		gitT(super, "add", "-A")
		gitT(super, "commit", "-q", "-m", "base")
		gitT(super, "submodule", "add", sub, "submod")
		gitT(super, "commit", "-q", "-m", "add submodule")
		writeFileT(super, "submod/inner.txt", "modified inner\n")
		_, err = gitagent.TakeSnapshot(ctx, super, gitagent.SnapshotPolicy{})
		Expect(err).To(MatchError(ContainSubstring("submodule")))

		conflict := GinkgoT().TempDir()
		gitT(conflict, "init", "-q")
		writeFileT(conflict, "c.txt", "base\n")
		gitT(conflict, "add", "-A")
		gitT(conflict, "commit", "-q", "-m", "base")
		gitT(conflict, "checkout", "-q", "-b", "side")
		writeFileT(conflict, "c.txt", "side\n")
		gitT(conflict, "commit", "-q", "-am", "side")
		gitT(conflict, "checkout", "-q", "main")
		writeFileT(conflict, "c.txt", "main\n")
		gitT(conflict, "commit", "-q", "-am", "main")
		cmd := exec.Command("git",
			"-c", "user.name=test", "-c", "user.email=test@localhost",
			"merge", "side")
		cmd.Dir = conflict
		_ = cmd.Run() // expected to conflict
		_, err = gitagent.TakeSnapshot(ctx, conflict, gitagent.SnapshotPolicy{})
		Expect(err).To(MatchError(ContainSubstring("unmerged")))
	})

	It("enforces snapshot caps", func() {
		dir := GinkgoT().TempDir()
		gitT(dir, "init", "-q")
		writeFileT(dir, "seed.txt", "seed\n")
		gitT(dir, "add", "-A")
		gitT(dir, "commit", "-q", "-m", "base")
		writeFileT(dir, "a.txt", "a\n")
		writeFileT(dir, "b.txt", "b\n")

		_, err := gitagent.TakeSnapshot(ctx, dir, gitagent.SnapshotPolicy{MaxFiles: 1})
		Expect(err).To(MatchError(ContainSubstring("cap")))

		_, err = gitagent.TakeSnapshot(ctx, dir, gitagent.SnapshotPolicy{MaxFileSize: 1})
		Expect(err).To(MatchError(ContainSubstring("cap")))
	})

	It("refuses a repository with no commits", func() {
		dir := GinkgoT().TempDir()
		gitT(dir, "init", "-q")
		_, err := gitagent.TakeSnapshot(ctx, dir, gitagent.SnapshotPolicy{})
		Expect(err).To(MatchError(ContainSubstring("at least one commit")))
	})
})
