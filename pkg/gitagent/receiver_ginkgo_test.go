package gitagent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
)

var _ = Describe("receiver repositories", func() {
	ctx := context.Background()

	It("configures a sidecar with the R2.2 block, idempotently", func() {
		path := filepath.Join(GinkgoT().TempDir(), "repo.git")
		Expect(gitagent.InitSidecar(ctx, path)).To(Succeed())
		Expect(gitagent.InitSidecar(ctx, path)).To(Succeed(), "re-running must be safe")

		for key, want := range map[string]string{
			"receive.advertisepushoptions": "true",
			"receive.fsckobjects":          "true",
			"receive.denydeletes":          "true",
			"receive.autogc":               "false",
			"core.logallrefupdates":        "always",
		} {
			Expect(gitT(path, "config", key)).To(Equal(want), key)
		}
		Expect(gitT(path, "config", "core.hookspath")).To(Equal(filepath.Join(path, "hooks")),
			"a pusher's global core.hooksPath must not bypass receiver hooks")
		Expect(gitT(path, "config", "receive.maxinputsize")).NotTo(Equal("0"), "maxInputSize must be finite")
	})

	It("namespaces mailboxes by canonical repository and refuses rebinding", func() {
		servedRoot := GinkgoT().TempDir()
		makeRepo := func(parent string) string {
			repo := filepath.Join(parent, "project")
			Expect(os.MkdirAll(repo, 0o755)).To(Succeed())
			gitT(repo, "init", "-q")
			writeFileT(repo, "a.txt", repo+"\n")
			gitT(repo, "add", "-A")
			gitT(repo, "commit", "-q", "-m", "base")
			return repo
		}
		repoA := makeRepo(GinkgoT().TempDir())
		repoB := makeRepo(GinkgoT().TempDir())
		mailboxA, err := gitagent.EnsureMailbox(ctx, servedRoot, repoA)
		Expect(err).NotTo(HaveOccurred())
		mailboxB, err := gitagent.EnsureMailbox(ctx, servedRoot, repoB)
		Expect(err).NotTo(HaveOccurred())
		Expect(mailboxA.Route).NotTo(Equal(mailboxB.Route), "same basenames must not collide")
		Expect(mailboxA.Route).To(HavePrefix(gitagent.MailboxesDir + "/"))

		binding, err := gitagent.LoadMailboxBinding(mailboxA.Path)
		Expect(err).NotTo(HaveOccurred())
		canonicalRepoA, err := filepath.EvalSymlinks(repoA)
		Expect(err).NotTo(HaveOccurred())
		Expect(binding.Repository).To(Equal(canonicalRepoA))
		Expect(gitagent.InitMailbox(ctx, mailboxA.Path, repoB)).
			To(MatchError(ContainSubstring("cannot rebind")))
	})

	It("shares the real repository's objects with the mailbox via alternates", func() {
		real := GinkgoT().TempDir()
		gitT(real, "init", "-q")
		writeFileT(real, "a.txt", "a\n")
		gitT(real, "add", "-A")
		gitT(real, "commit", "-q", "-m", "base")
		head := gitT(real, "rev-parse", "HEAD")

		mailbox := filepath.Join(GinkgoT().TempDir(), "mailbox.git")
		Expect(gitagent.InitMailbox(ctx, mailbox, real)).To(Succeed())
		Expect(gitagent.InitMailbox(ctx, mailbox, real)).To(Succeed())

		alternates, err := os.ReadFile(filepath.Join(mailbox, "objects", "info", "alternates"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(alternates))).To(HaveSuffix(filepath.Join(".git", "objects")))
		// The real repo's commits are readable without any copy.
		Expect(gitT(mailbox, "cat-file", "-t", head)).To(Equal("commit"))
	})
})

var _ = Describe("hook shims", func() {
	It("installs idempotent pre/post-receive shims and refuses to clobber foreign hooks", func() {
		ctx := context.Background()
		repo := filepath.Join(GinkgoT().TempDir(), "repo.git")
		Expect(gitagent.InitSidecar(ctx, repo)).To(Succeed())

		Expect(gitagent.InstallHookShims(repo, "/usr/local/bin/captain", "/home/agent/.captain.yaml", gitagent.RoleSidecar, "worker-pool")).To(Succeed())
		Expect(gitagent.InstallHookShims(repo, "/usr/local/bin/captain", "/home/agent/.captain.yaml", gitagent.RoleSidecar, "worker-pool")).To(Succeed())

		for _, hook := range []string{"pre-receive", "post-receive"} {
			path := filepath.Join(repo, "hooks", hook)
			info, err := os.Stat(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode() & 0o111).NotTo(BeZero())
			content, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(ContainSubstring("/usr/local/bin/captain"))
			Expect(string(content)).To(ContainSubstring("--role \"sidecar\""))
			Expect(string(content)).To(ContainSubstring("--backend \"worker-pool\""))
			// A hook runs as a child of whoever pushed, so its config path is
			// baked in rather than resolved from an ambient $HOME.
			Expect(string(content)).To(ContainSubstring("--config \"/home/agent/.captain.yaml\""))
		}

		// A rebinned captain updates the shim in place.
		Expect(gitagent.InstallHookShims(repo, "/opt/captain", "/home/agent/.captain.yaml", gitagent.RoleSidecar, "worker-pool")).To(Succeed())
		content, err := os.ReadFile(filepath.Join(repo, "hooks", "pre-receive"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("/opt/captain"))

		// A hook captain did not install is never overwritten.
		foreign := filepath.Join(repo, "hooks", "pre-receive")
		Expect(os.WriteFile(foreign, []byte("#!/bin/sh\nexit 0\n"), 0o755)).To(Succeed())
		err = gitagent.InstallHookShims(repo, "/opt/captain", "/home/agent/.captain.yaml", gitagent.RoleSidecar, "worker-pool")
		Expect(err).To(MatchError(ContainSubstring("not installed by captain")))
	})
})
