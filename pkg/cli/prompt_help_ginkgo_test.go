package cli

import (
	"bytes"
	"strings"

	"github.com/flanksource/clicky"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("prompt help", func() {
	BeforeEach(func() {
		GinkgoT().Setenv("CLICKY_FORCE_INTERACTIVE", "1")
		GinkgoT().Setenv("NO_COLOR", "")
		GinkgoT().Setenv("TERM", "xterm-256color")
		GinkgoT().Setenv("COLOR", "")
	})
	clearSessionMarkers := func() {
		for _, marker := range []string{
			"CODEX_THREAD_ID", "CODEX_SESSION_ID", "CODEX_SANDBOX",
			"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "CLAUDECODE",
			"GEMINI_SESSION_ID", "GEMINI_CLI", "CAPTAIN_SESSION_ID",
		} {
			GinkgoT().Setenv(marker, "")
		}
	}

	promptCommand := func() *cobra.Command {
		return &cobra.Command{Use: "prompt", Short: "Manage prompt resources"}
	}

	It("renders colored terminal help for a human session", func() {
		output, err := renderPromptHelp(promptCommand(), promptHelpRenderOptions{})

		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("Captain .prompt Files"))
		Expect(output).To(ContainSubstring("\x1b["))
	})

	It("renders uncolored Markdown help for an LLM session", func() {
		output, err := renderPromptHelp(promptCommand(), promptHelpRenderOptions{LLMSession: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("# Captain .prompt Files"))
		Expect(output).NotTo(ContainSubstring("\x1b["))
	})

	DescribeTable("honors plain help output", func(name, reason string) {
		switch reason {
		case "environment":
			GinkgoT().Setenv("NO_COLOR", "1")
		case "redirected":
			GinkgoT().Setenv("CLICKY_FORCE_INTERACTIVE", "")
		}
		output, err := renderPromptHelp(&cobra.Command{Use: name}, promptHelpRenderOptions{NoColor: reason == "flag"})

		Expect(err).NotTo(HaveOccurred())
		Expect(output).NotTo(BeEmpty())
		Expect(output).NotTo(ContainSubstring("\x1b["))
	},
		Entry("root flag", "prompt", "flag"),
		Entry("root environment", "prompt", "environment"),
		Entry("root redirected", "prompt", "redirected"),
		Entry("child flag", "run", "flag"),
		Entry("child environment", "run", "environment"),
		Entry("child redirected", "run", "redirected"),
	)

	It("documents the complete prompt-file authoring contract", func() {
		output, err := renderPromptHelp(promptCommand(), promptHelpRenderOptions{LLMSession: true})

		Expect(err).NotTo(HaveOccurred())
		for _, required := range []string{
			"YAML frontmatter",
			"Handlebars body",
			`{{role "system"}}`,
			"config.maxOutputTokens",
			"input.schema",
			"input.default",
			"output.schema",
			"runtimes[]",
			"fallbacks[]",
			"prompt.schemaStrictness",
			"permissions.tools.<tool>",
			"permissions.access",
			"presets[]",
			"--no-color",
			"captain prompt observe",
			"permissions.mcp.<server>",
			"memory.skipProject",
			"setup.checkout.worktree.uncommitted",
			"sandbox.policy.maxAttempts",
			"workflow.verify.maxIterations",
			"workflow.commits[].gates",
			"toolPreferences.<tool-or-group>",
			"toolApproval",
			"cliArgs",
			"captain prompt --schema",
			"captain prompt render",
			"captain prompt run",
		} {
			Expect(output).To(ContainSubstring(required), "missing prompt help detail %q", required)
		}
		Expect(strings.Count(output, "```yaml")).To(BeNumerically(">=", 1))
	})

	It("selects Markdown when an LLM environment marker is present", func() {
		clearSessionMarkers()
		GinkgoT().Setenv("CODEX_THREAD_ID", "thread-example")
		root := &cobra.Command{Use: "captain"}
		root.AddCommand(promptCommand())
		Expect(AttachPromptHelp(root)).To(Succeed())
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&output)
		root.SetArgs([]string{"prompt", "--help"})

		Expect(root.Execute()).To(Succeed())
		Expect(output.String()).To(HavePrefix("# Captain .prompt Files"))
		Expect(output.String()).NotTo(ContainSubstring("\x1b["))
		Expect(output.String()).NotTo(ContainSubstring("style="))
	})

	DescribeTable("renders help throughout the prompt command tree", func(name string, llm, noColor bool) {
		clearSessionMarkers()
		if llm {
			GinkgoT().Setenv("CODEX_THREAD_ID", "thread-example")
		}
		previous := clicky.Flags.NoColor
		DeferCleanup(func() { clicky.Flags.NoColor = previous })
		clicky.Flags.NoColor = noColor
		root := &cobra.Command{Use: "captain"}
		prompt := promptCommand()
		child := &cobra.Command{Use: name + " [id]", Short: "Manage " + name, Run: func(*cobra.Command, []string) {}}
		child.Flags().String("query", "", "Filter by name")
		prompt.AddCommand(child)
		root.AddCommand(prompt)
		Expect(AttachPromptHelp(root)).To(Succeed())
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetArgs([]string{"prompt", name, "--help"})

		Expect(root.Execute()).To(Succeed())
		Expect(output.String()).To(ContainSubstring("captain prompt " + name))
		Expect(output.String()).To(ContainSubstring("--query"))
		Expect(output.String()).NotTo(ContainSubstring("Captain .prompt Files"))
		if llm {
			Expect(output.String()).To(HavePrefix("# captain prompt " + name))
		}
		if llm || noColor {
			Expect(output.String()).NotTo(ContainSubstring("\x1b["))
		} else {
			Expect(output.String()).To(ContainSubstring("\x1b["))
			Expect(output.String()).To(ContainSubstring("38;2;"))
		}
	},
		Entry("run on a terminal", "run", false, false),
		Entry("render on a terminal", "render", false, false),
		Entry("list on a terminal", "list", false, false),
		Entry("get on a terminal", "get", false, false),
		Entry("create on a terminal", "create", false, false),
		Entry("update on a terminal", "update", false, false),
		Entry("delete on a terminal", "delete", false, false),
		Entry("observe on a terminal", "observe", false, false),
		Entry("run in an agent session", "run", true, false),
		Entry("render without color", "render", false, true),
	)
})
