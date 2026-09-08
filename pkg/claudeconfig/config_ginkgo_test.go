package claudeconfig_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/claudeconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestClaudeConfigGinkgo(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Claude Config Suite")
}

var _ = Describe("claude sandbox allowWrite", func() {
	var path string

	writeSettings := func(content string) {
		path = filepath.Join(GinkgoT().TempDir(), "settings.json")
		if content != "" {
			ExpectWithOffset(1, os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		}
		claudeconfig.SetPathForTesting(path)
		DeferCleanup(func() { claudeconfig.SetPathForTesting("") })
	}

	read := func() map[string]any {
		content, err := os.ReadFile(path)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		var settings map[string]any
		ExpectWithOffset(1, json.Unmarshal(content, &settings)).To(Succeed())
		return settings
	}

	allowWrite := func() []any {
		sandbox := read()["sandbox"].(map[string]any)
		return sandbox["filesystem"].(map[string]any)["allowWrite"].([]any)
	}

	It("appends to an existing allowlist and preserves every other setting", func() {
		writeSettings(`{"model":"opus","sandbox":{"enabled":true,"filesystem":{"allowWrite":["/tmp"]},` +
			`"excludedCommands":["git *"]},"hooks":{"Stop":[]}}`)

		added, err := claudeconfig.EnsureSandboxWritable([]string{"/Users/someone/.captain/browser"})

		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(ConsistOf("/Users/someone/.captain/browser"))
		Expect(allowWrite()).To(Equal([]any{"/tmp", "/Users/someone/.captain/browser"}))
		settings := read()
		Expect(settings["model"]).To(Equal("opus"))
		Expect(settings["hooks"]).To(HaveKey("Stop"))
		sandbox := settings["sandbox"].(map[string]any)
		Expect(sandbox["enabled"]).To(BeTrue())
		Expect(sandbox["excludedCommands"]).To(Equal([]any{"git *"}))
	})

	It("is idempotent", func() {
		writeSettings(`{"sandbox":{"filesystem":{"allowWrite":["/a"]}}}`)
		_, err := claudeconfig.EnsureSandboxWritable([]string{"/a"})

		Expect(err).NotTo(HaveOccurred())
		Expect(allowWrite()).To(Equal([]any{"/a"}))
	})

	It("creates the sandbox block when settings exist but declare none", func() {
		writeSettings(`{"model":"opus"}`)

		_, err := claudeconfig.EnsureSandboxWritable([]string{"/a"})

		Expect(err).NotTo(HaveOccurred())
		Expect(allowWrite()).To(Equal([]any{"/a"}))
		Expect(read()["model"]).To(Equal("opus"))
	})

	It("creates the settings file on a machine that has never run Claude Code", func() {
		writeSettings("")

		added, err := claudeconfig.EnsureSandboxWritable([]string{"/a"})

		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(ConsistOf("/a"))
		Expect(allowWrite()).To(Equal([]any{"/a"}))
	})

	It("refuses to rewrite settings it cannot parse", func() {
		writeSettings("{not json")

		_, err := claudeconfig.EnsureSandboxWritable([]string{"/a"})

		Expect(err).To(MatchError(ContainSubstring("parse")))
	})

	It("refuses an allowWrite key that is not a list", func() {
		writeSettings(`{"sandbox":{"filesystem":{"allowWrite":"/a"}}}`)

		_, err := claudeconfig.EnsureSandboxWritable([]string{"/b"})

		Expect(err).To(MatchError(ContainSubstring("allowWrite")))
	})

	It("does not touch the file when nothing needs adding", func() {
		writeSettings(`{"sandbox":{"filesystem":{"allowWrite":["/a"]}}}`)
		before, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())

		added, err := claudeconfig.EnsureSandboxWritable([]string{"/a"})

		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(BeEmpty())
		after, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.ModTime()).To(Equal(before.ModTime()))
	})
})
