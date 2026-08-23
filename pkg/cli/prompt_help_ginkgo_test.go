package cli

import (
	"bytes"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("prompt help", func() {
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
})
