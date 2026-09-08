package codexconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/codexconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCodexConfigGinkgo(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Codex Config Suite")
}

// realWorldConfig is the shape codex writes and users then annotate: a
// multi-line array with a trailing comment, inside a table, with other tables
// after it.
const realWorldConfig = `model = "gpt-5.6"
max_threads = 8

[sandbox_workspace_write]
exclude_slash_tmp = false # Allow /tmp
writable_roots = [
  "~/go/pkg/",
  "/Users/someone/Downloads",
] # Allow writing to these directories
network_access = true

[projects."/repo"]
trust_level = "trusted"
`

var _ = Describe("codex writable_roots", func() {
	var path string

	writeConfig := func(content string) {
		path = filepath.Join(GinkgoT().TempDir(), "config.toml")
		if content != "" {
			ExpectWithOffset(1, os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		}
		codexconfig.SetPathForTesting(path)
		DeferCleanup(func() { codexconfig.SetPathForTesting("") })
	}

	read := func() string {
		content, err := os.ReadFile(path)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		return string(content)
	}

	It("inserts into a multi-line array without disturbing comments or ordering", func() {
		writeConfig(realWorldConfig)

		added, err := codexconfig.EnsureWritableRoots([]string{"/Users/someone/.captain/browser"})

		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(ConsistOf("/Users/someone/.captain/browser"))
		Expect(read()).To(Equal(`model = "gpt-5.6"
max_threads = 8

[sandbox_workspace_write]
exclude_slash_tmp = false # Allow /tmp
writable_roots = [
  "~/go/pkg/",
  "/Users/someone/Downloads",
  "/Users/someone/.captain/browser",
] # Allow writing to these directories
network_access = true

[projects."/repo"]
trust_level = "trusted"
`))
	})

	// codex's own default config omits the comma on the last element, so
	// appending after it has to close that element first or the file stops
	// parsing. The original fixture had a trailing comma and hid this.
	It("closes an unterminated last element before appending", func() {
		writeConfig("[sandbox_workspace_write]\nwritable_roots = [\n  \"/a\",\n  \"/b\"\n] # trailing\nnetwork_access = true\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/c"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(Equal("[sandbox_workspace_write]\nwritable_roots = [\n  \"/a\",\n  \"/b\",\n  \"/c\",\n] # trailing\nnetwork_access = true\n"))
	})

	It("re-parses cleanly after appending to an unterminated array", func() {
		writeConfig("[sandbox_workspace_write]\nwritable_roots = [\n  \"/a\"\n]\n")
		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})
		Expect(err).NotTo(HaveOccurred())

		// A second call parses the file it just wrote; an invalid document fails here.
		added, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(BeEmpty())
	})

	It("puts the comma before a per-entry comment, not after it", func() {
		writeConfig("[sandbox_workspace_write]\nwritable_roots = [\n  \"/a\" # why\n]\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(Equal("[sandbox_workspace_write]\nwritable_roots = [\n  \"/a\", # why\n  \"/b\",\n]\n"))
	})

	It("does not mistake a '#' inside a quoted path for a comment", func() {
		writeConfig("[sandbox_workspace_write]\nwritable_roots = [\n  \"/tmp/c#1\"\n]\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(Equal("[sandbox_workspace_write]\nwritable_roots = [\n  \"/tmp/c#1\",\n  \"/b\",\n]\n"))
	})

	It("skips trailing blank and comment lines when closing the last element", func() {
		writeConfig("[sandbox_workspace_write]\nwritable_roots = [\n  \"/a\"\n  # a note\n\n]\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(Equal("[sandbox_workspace_write]\nwritable_roots = [\n  \"/a\",\n  # a note\n\n  \"/b\",\n]\n"))
	})

	It("is idempotent", func() {
		writeConfig(realWorldConfig)
		_, err := codexconfig.EnsureWritableRoots([]string{"/Users/someone/.captain/browser"})
		Expect(err).NotTo(HaveOccurred())
		first := read()

		added, err := codexconfig.EnsureWritableRoots([]string{"/Users/someone/.captain/browser"})

		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(BeEmpty())
		Expect(read()).To(Equal(first))
	})

	It("treats a home-relative root already present as covering its absolute form", func() {
		home, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		writeConfig("[sandbox_workspace_write]\nwritable_roots = [\n  \"~/.captain/browser\",\n]\n")

		added, err := codexconfig.EnsureWritableRoots([]string{filepath.Join(home, ".captain", "browser")})

		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(BeEmpty())
	})

	It("extends a single-line array and keeps its trailing comment", func() {
		writeConfig("[sandbox_workspace_write]\nwritable_roots = [\"/a\"] # keep me\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(Equal("[sandbox_workspace_write]\nwritable_roots = [\"/a\", \"/b\"] # keep me\n"))
	})

	It("fills an empty single-line array without a leading comma", func() {
		writeConfig("[sandbox_workspace_write]\nwritable_roots = []\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(Equal("[sandbox_workspace_write]\nwritable_roots = [\"/b\"]\n"))
	})

	It("adds the key to a table that does not declare it", func() {
		writeConfig("[sandbox_workspace_write]\nnetwork_access = true\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(Equal("[sandbox_workspace_write]\nwritable_roots = [\n  \"/b\",\n]\nnetwork_access = true\n"))
	})

	It("appends the table to a config that has no sandbox settings", func() {
		writeConfig("model = \"gpt-5.6\"\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(Equal("model = \"gpt-5.6\"\n\n[sandbox_workspace_write]\nwritable_roots = [\n  \"/b\",\n]\n"))
	})

	It("creates the config file when codex has never been configured", func() {
		writeConfig("")

		added, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(ConsistOf("/b"))
		Expect(read()).To(Equal("[sandbox_workspace_write]\nwritable_roots = [\n  \"/b\",\n]\n"))
	})

	It("refuses to edit a config it cannot parse", func() {
		writeConfig("this is not = = toml\n")

		_, err := codexconfig.EnsureWritableRoots([]string{"/b"})

		Expect(err).To(MatchError(ContainSubstring("parse")))
	})

	It("leaves the notify entry alone", func() {
		writeConfig("notify = [\"captain\", \"hook\", \"monitor\", \"notify\"]\n\n" + realWorldConfig)

		_, err := codexconfig.EnsureWritableRoots([]string{"/new"})

		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(ContainSubstring(`notify = ["captain", "hook", "monitor", "notify"]`))
	})
})
