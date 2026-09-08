package agentsandbox_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/agentsandbox"
	"github.com/flanksource/captain/pkg/claudeconfig"
	"github.com/flanksource/captain/pkg/codexconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAgentSandboxGinkgo(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Sandbox Suite")
}

var _ = Describe("EnsureWritable", func() {
	var claudePath, codexPath string

	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		claudePath = filepath.Join(dir, "settings.json")
		codexPath = filepath.Join(dir, "config.toml")
		claudeconfig.SetPathForTesting(claudePath)
		codexconfig.SetPathForTesting(codexPath)
		DeferCleanup(func() {
			claudeconfig.SetPathForTesting("")
			codexconfig.SetPathForTesting("")
		})
	})

	read := func(path string) string {
		content, err := os.ReadFile(path)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		return string(content)
	}

	It("registers the path with both agents in one call", func() {
		target := filepath.Join(GinkgoT().TempDir(), "browser")

		changes, err := agentsandbox.EnsureWritable(target)

		Expect(err).NotTo(HaveOccurred())
		Expect(changes).To(HaveLen(2))
		Expect(changes[0].Agent).To(Equal("claude"))
		Expect(changes[0].Added).To(ConsistOf(target))
		Expect(changes[1].Agent).To(Equal("codex"))
		Expect(changes[1].Added).To(ConsistOf(target))
		Expect(read(claudePath)).To(ContainSubstring(target))
		Expect(read(codexPath)).To(ContainSubstring(target))
	})

	It("makes a relative path absolute, because each allowlist resolves it against the agent's own cwd", func() {
		changes, err := agentsandbox.EnsureWritable("relative/state")

		Expect(err).NotTo(HaveOccurred())
		absolute, err := filepath.Abs("relative/state")
		Expect(err).NotTo(HaveOccurred())
		Expect(changes[0].Added).To(ConsistOf(absolute))
	})

	It("reports no additions on a second run", func() {
		target := filepath.Join(GinkgoT().TempDir(), "browser")
		_, err := agentsandbox.EnsureWritable(target)
		Expect(err).NotTo(HaveOccurred())

		changes, err := agentsandbox.EnsureWritable(target)

		Expect(err).NotTo(HaveOccurred())
		for _, change := range changes {
			Expect(change.Added).To(BeEmpty())
		}
	})

	It("still configures codex when the claude settings file is unusable", func() {
		Expect(os.WriteFile(claudePath, []byte("{not json"), 0o600)).To(Succeed())
		target := filepath.Join(GinkgoT().TempDir(), "browser")

		changes, err := agentsandbox.EnsureWritable(target)

		Expect(err).To(HaveOccurred())
		Expect(changes).To(HaveLen(1))
		Expect(changes[0].Agent).To(Equal("codex"))
		Expect(read(codexPath)).To(ContainSubstring(target))
	})

	It("does nothing when given no paths", func() {
		changes, err := agentsandbox.EnsureWritable()

		Expect(err).NotTo(HaveOccurred())
		Expect(changes).To(BeEmpty())
		Expect(claudePath).NotTo(BeAnExistingFile())
	})
})
