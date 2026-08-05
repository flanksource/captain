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

		Expect(gitagent.InstallHookShims(repo, "/usr/local/bin/captain", "/home/agent/.captain.yaml", gitagent.RoleSidecar)).To(Succeed())
		Expect(gitagent.InstallHookShims(repo, "/usr/local/bin/captain", "/home/agent/.captain.yaml", gitagent.RoleSidecar)).To(Succeed())

		for _, hook := range []string{"pre-receive", "post-receive"} {
			path := filepath.Join(repo, "hooks", hook)
			info, err := os.Stat(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode() & 0o111).NotTo(BeZero())
			content, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(ContainSubstring("/usr/local/bin/captain"))
			Expect(string(content)).To(ContainSubstring("--role \"sidecar\""))
			// A hook runs as a child of whoever pushed, so its config path is
			// baked in rather than resolved from an ambient $HOME.
			Expect(string(content)).To(ContainSubstring("--config \"/home/agent/.captain.yaml\""))
		}

		// A rebinned captain updates the shim in place.
		Expect(gitagent.InstallHookShims(repo, "/opt/captain", "/home/agent/.captain.yaml", gitagent.RoleSidecar)).To(Succeed())
		content, err := os.ReadFile(filepath.Join(repo, "hooks", "pre-receive"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("/opt/captain"))

		// A hook captain did not install is never overwritten.
		foreign := filepath.Join(repo, "hooks", "pre-receive")
		Expect(os.WriteFile(foreign, []byte("#!/bin/sh\nexit 0\n"), 0o755)).To(Succeed())
		err = gitagent.InstallHookShims(repo, "/opt/captain", "/home/agent/.captain.yaml", gitagent.RoleSidecar)
		Expect(err).To(MatchError(ContainSubstring("not installed by captain")))
	})
})
