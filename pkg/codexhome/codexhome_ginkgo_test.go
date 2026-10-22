package codexhome

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCodexHome(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Codex Home Suite")
}

func writeFile(path, body string) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
}

var _ = Describe("read-only CODEX_HOME", func() {
	var user, config string

	BeforeEach(func() {
		root := GinkgoT().TempDir()
		user, config = filepath.Join(root, "user-codex"), filepath.Join(root, "config")
		GinkgoT().Setenv("CODEX_HOME", user)
		GinkgoT().Setenv("XDG_CONFIG_HOME", config)
		writeFile(filepath.Join(user, "auth.json"), `{"tokens":{}}`)
		writeFile(filepath.Join(user, "config.toml"), "model = \"gpt-6.1-sol\"\n")
		writeFile(filepath.Join(user, "rules", "default.rules"), `prefix_rule(pattern=["git","add"], decision="allow")`)
	})

	It("links what codex needs from the user's home and carries over no rules", func() {
		home, err := PrepareReadOnly()
		Expect(err).NotTo(HaveOccurred())

		Expect(home).To(Equal(filepath.Join(config, "captain", "codex-read-only")))
		for _, name := range []string{"auth.json", "config.toml"} {
			target, err := os.Readlink(filepath.Join(home, name))
			Expect(err).NotTo(HaveOccurred())
			Expect(target).To(Equal(filepath.Join(user, name)))
		}
		Expect(filepath.Join(home, "rules")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(home, "AGENTS.md")).NotTo(BeAnExistingFile(), "a missing source is not linked")
	})

	It("refuses a home where something has written rules", func() {
		home, err := PrepareReadOnly()
		Expect(err).NotTo(HaveOccurred())
		writeFile(filepath.Join(home, "rules", "default.rules"), `prefix_rule(pattern=["git"], decision="allow")`)

		_, err = PrepareReadOnly()
		Expect(err).To(MatchError(ContainSubstring(filepath.Join(home, "rules"))))
	})

	Describe("a login codex replaced the link with", func() {
		var home, isolated string
		BeforeEach(func() {
			var err error
			home, err = PrepareReadOnly()
			Expect(err).NotTo(HaveOccurred())
			isolated = filepath.Join(home, "auth.json")
			Expect(os.Remove(isolated)).To(Succeed())
			writeFile(isolated, `{"tokens":{"refresh_token":"isolated"}}`)
		})

		It("moves a newer login back over the user's and re-links it", func() {
			past := time.Now().Add(-time.Hour)
			Expect(os.Chtimes(filepath.Join(user, "auth.json"), past, past)).To(Succeed())

			_, err := PrepareReadOnly()
			Expect(err).NotTo(HaveOccurred())

			Expect(os.ReadFile(filepath.Join(user, "auth.json"))).To(Equal([]byte(`{"tokens":{"refresh_token":"isolated"}}`)))
			Expect(os.Readlink(isolated)).To(Equal(filepath.Join(user, "auth.json")))
		})

		It("re-links over an older copy and keeps the user's newer login", func() {
			past := time.Now().Add(-time.Hour)
			Expect(os.Chtimes(isolated, past, past)).To(Succeed())

			_, err := PrepareReadOnly()
			Expect(err).NotTo(HaveOccurred())

			Expect(os.ReadFile(filepath.Join(user, "auth.json"))).To(Equal([]byte(`{"tokens":{}}`)))
			Expect(os.Readlink(isolated)).To(Equal(filepath.Join(user, "auth.json")))
		})
	})

	It("refuses a linked entry that became a regular file rather than deleting it", func() {
		home, err := PrepareReadOnly()
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Remove(filepath.Join(home, "config.toml"))).To(Succeed())
		writeFile(filepath.Join(home, "config.toml"), "model = \"edited\"\n")

		_, err = PrepareReadOnly()
		Expect(err).To(MatchError(ContainSubstring(filepath.Join(home, "config.toml"))))
	})

	It("lists the read-only home's sessions beside the user's", func() {
		roots, err := SessionRoots()
		Expect(err).NotTo(HaveOccurred())
		Expect(roots).To(ContainElement(filepath.Join(config, "captain", "codex-read-only", "sessions")))
	})
})

var _ = Describe("project execpolicy rules", func() {
	DescribeTable("refuses a project rules file that allows a command",
		func(files map[string]string, cwd string, want string) {
			root := GinkgoT().TempDir()
			for path, body := range files {
				writeFile(filepath.Join(root, path), body)
			}
			err := RequireNoProjectAllowRules(filepath.Join(root, cwd))
			if want == "" {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(ContainSubstring(filepath.Join(root, want))))
		},
		Entry("an allow rule at the repository root, run from a subdirectory",
			map[string]string{"repo/.git/HEAD": "", "repo/.codex/rules/team.rules": `prefix_rule(pattern=["git","add"], decision="allow")`, "repo/sub/x": ""},
			"repo/sub", "repo/.codex/rules/team.rules"),
		Entry("a rule with no decision, which codex treats as allow",
			map[string]string{"repo/.git/HEAD": "", "repo/.codex/rules/team.rules": `prefix_rule(pattern=["npx"])`},
			"repo", "repo/.codex/rules/team.rules"),
		Entry("forbidden and prompt rules only",
			map[string]string{"repo/.git/HEAD": "", "repo/.codex/rules/team.rules": "prefix_rule(pattern=[\"rm\"], decision=\"forbidden\")\nprefix_rule(pattern=[\"git\"], decision=\"prompt\")"},
			"repo", ""),
		Entry("rules above the repository root are not codex's project layers",
			map[string]string{".codex/rules/outer.rules": `prefix_rule(pattern=["git"], decision="allow")`, "repo/.git/HEAD": ""},
			"repo", ""),
	)
})
