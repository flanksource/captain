package cli

import (
	"reflect"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/flags"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("grouped runtime flags", func() {
	BeforeEach(func() {
		previous := *clicky.Flags
		DeferCleanup(func() { *clicky.Flags = previous })
	})

	DescribeTable("categorizes shared runtime options in prompt help", func(optionsType reflect.Type, llm bool) {
		root := &cobra.Command{Use: "captain"}
		clicky.BindAllFlagsToCommand(root, "format")
		prompt := &cobra.Command{Use: "prompt"}
		child := &cobra.Command{Use: "render [id]", Run: func(*cobra.Command, []string) {}}
		fields, err := flags.ParseStructFields(optionsType)
		Expect(err).NotTo(HaveOccurred())
		for _, field := range fields {
			flags.BindFlag(child, field)
		}
		prompt.AddCommand(child)
		root.AddCommand(prompt)
		child.InitDefaultHelpFlag()
		output, err := renderPromptHelp(child, promptHelpRenderOptions{LLMSession: llm, NoColor: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).NotTo(ContainSubstring("\x1b["))
		sections := map[string][]string{
			"Command flags":     {"--prompt", "--var", "--attach", "--system"},
			"Model selection":   {"--model", "--fallback", "--mode", "--effort", "--temperature", "--multi-models", "--no-cache"},
			"Model permissions": {"--access", "--perms", "--permission-mode", "--sandbox", "--edit", "--allowed-tools", "--disallowed-tools", "--no-mcp"},
			"Model runtime":     {"--api-key", "--api-url", "--budget", "--max-tokens", "--max-turns", "--timeout", "--resume", "--preset", "--no-presets", "--no-stream", "--no-hooks", "--no-memory"},
			"Formatting":        {"--json", "--format", "--no-color"},
			"Logging":           {"--log-level", "--json-logs"},
		}
		for heading, expected := range sections {
			_, section, found := strings.Cut(output, heading+":\n")
			Expect(found).To(BeTrue(), heading)
			section, _, _ = strings.Cut(section, "\n\n")
			for _, name := range expected {
				Expect(section).To(ContainSubstring(name), "%s must contain %s", heading, name)
			}
		}
	},
		Entry("typed AI prompt", reflect.TypeFor[AIPromptOptions](), false),
		Entry("entity prompt action", reflect.TypeFor[PromptActionFlags](), false),
		Entry("entity prompt action in an agent session", reflect.TypeFor[PromptActionFlags](), true),
	)
})
