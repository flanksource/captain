package gitagent_test

import (
	"context"
	"os"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A4.3 audit of commons-db's dirty-state mechanism (shell.Checkout.Dirty →
// applyDirtyState), which issue #39 §4 names as the dispatch-snapshot
// substrate. The dispatch snapshot does NOT use it — TakeSnapshot builds the
// commit from git plumbing directly — but Spec.Setup.Checkout.Dirty remains a
// user-facing surface, so this suite pins what round-trips and what does not.
// Failures here after a commons-db upgrade mean upstream behaviour changed:
// re-audit before trusting it. Known gaps (filed upstream rather than forked,
// A4.2): skip-worktree edits are silently dropped, CRLF normalization is not
// pinned, LFS pointers pass through silently, and dirtyFiles mangles unusual
// paths.
var _ = Describe("commons-db dirty-state audit (A4.3)", func() {
	prepare := func(src string) (*shell.SetupResult, error) {
		return shell.Prepare(dbcontext.NewContext(context.Background()), &shell.Setup{
			BaseDir: GinkgoT().TempDir(),
			Checkout: &shell.Checkout{
				Path:     src,
				Dirty:    &shell.Dirty{Stash: shell.StashAll},
				Worktree: &shell.Worktree{Mode: shell.WorktreeNew, Prefix: "captain-audit"},
			},
		})
	}

	It("round-trips staged deletions, renames, exec bits, symlinks and untracked files", func() {
		src := newFidelityRepo()
		// The fidelity fixture arms core.autocrlf=true + a text attribute;
		// commons-db does not pin normalization (a known gap), so drop the
		// CRLF tripwires to audit the rest in isolation.
		gitT(src, "config", "core.autocrlf", "false")
		Expect(os.Remove(filepath.Join(src, ".gitattributes"))).To(Succeed())

		res, err := prepare(src)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			if res.Cleanup != nil {
				_ = res.Cleanup()
			}
		})
		wt := res.Cwd
		Expect(wt).NotTo(Equal(src))

		Expect(os.ReadFile(filepath.Join(wt, "mod.txt"))).To(Equal([]byte("v2\n")))
		Expect(filepath.Join(wt, "del-staged.txt")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(wt, "ren-old.txt")).NotTo(BeAnExistingFile())
		Expect(os.ReadFile(filepath.Join(wt, "ren-new.txt"))).To(Equal([]byte("renamed v2\n")))
		info, err := os.Stat(filepath.Join(wt, "exec.sh"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode()&0o111).NotTo(BeZero(), "exec bit lost in transit")
		target, err := os.Readlink(filepath.Join(wt, "link.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("mod.txt"))
		Expect(os.ReadFile(filepath.Join(wt, "newdir/new.txt"))).To(Equal([]byte("brand new\n")))
		Expect(os.ReadFile(filepath.Join(wt, "with space.txt"))).To(Equal([]byte("spaced\n")))
	})

	It("silently drops skip-worktree edits — the pinned upstream gap", func() {
		src := GinkgoT().TempDir()
		gitT(src, "init", "-q")
		writeFileT(src, "hidden.txt", "committed\n")
		writeFileT(src, "visible.txt", "v1\n")
		gitT(src, "add", "-A")
		gitT(src, "commit", "-q", "-m", "base")
		writeFileT(src, "hidden.txt", "local edit git cannot see\n")
		gitT(src, "update-index", "--skip-worktree", "hidden.txt")
		writeFileT(src, "visible.txt", "v2\n")

		res, err := prepare(src)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			if res.Cleanup != nil {
				_ = res.Cleanup()
			}
		})
		Expect(os.ReadFile(filepath.Join(res.Cwd, "visible.txt"))).To(Equal([]byte("v2\n")))
		// Documented loss: `git diff` does not report skip-worktree paths, so
		// the local edit never reaches the worktree. When an upstream release
		// starts carrying it, this assertion breaks — re-audit then.
		Expect(os.ReadFile(filepath.Join(res.Cwd, "hidden.txt"))).To(Equal([]byte("committed\n")),
			"commons-db now carries skip-worktree edits; update the audit and the upstream-gap list")
	})
})
