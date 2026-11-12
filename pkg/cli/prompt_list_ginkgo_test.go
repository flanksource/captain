package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/clicky"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("Prompt list lookup locations", func() {
	BeforeEach(func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(captainconfig.SetPathForTesting, "")
		GinkgoT().Setenv("XDG_CONFIG_HOME", GinkgoT().TempDir())
		GinkgoT().Chdir(GinkgoT().TempDir())
	})

	It("shows embedded sources and nonexistent project and user lookup directories", func() {
		cwd, err := filepath.Abs(".")
		Expect(err).NotTo(HaveOccurred())
		home, err := captainconfig.Home()
		Expect(err).NotTo(HaveOccurred())
		out, err := promptLookupFooter(context.Background(), clicky.FormatOptions{NoColor: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("Lookup locations"))
		Expect(out).NotTo(ContainSubstring("Built-in prompts"))
		Expect(out).To(ContainSubstring("Embedded examples"))
		Expect(out).To(ContainSubstring(filepath.Join(cwd, ".captain", "prompts")))
		Expect(out).To(ContainSubstring(filepath.Join(home, "prompts")))
	})

	It("shows configured and host sources ahead of the workspace prompt directory", func() {
		configPath, err := captainconfig.Path()
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Mkdir(filepath.Join(filepath.Dir(configPath), "configured-prompts"), 0o700)).To(Succeed())
		Expect(os.Mkdir("host-prompts", 0o700)).To(Succeed())
		Expect(captainconfig.Save(captainconfig.Config{Prompts: captainconfig.PromptDefaults{Dirs: []string{"configured-prompts"}}})).To(Succeed())
		ctx := ContextWithPromptSources(context.Background(), PromptSources{Dirs: []string{"host-prompts"}})
		out, err := promptLookupFooter(ctx, clicky.FormatOptions{Format: "markdown"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("**Lookup locations**"))
		// Markdown escapes "_" in the temp paths (CI's /home/runner/work/_temp),
		// so order is checked on the unescaped directory names.
		configured := strings.Index(out, "configured-prompts")
		host := strings.Index(out, "host-prompts")
		workspace := strings.Index(out, filepath.Join(".captain", "prompts"))
		Expect(configured).To(BeNumerically(">=", 0), out)
		Expect(host).To(BeNumerically(">", configured), out)
		Expect(workspace).To(BeNumerically(">", host), "the workspace directory is always a source, after configured and host ones")
	})

	DescribeTable("keeps machine output and file exports free of a terminal footer", func(opts clicky.FormatOptions) {
		out, err := promptLookupFooter(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(BeEmpty())
	},
		Entry("JSON", clicky.FormatOptions{JSON: true}),
		Entry("YAML", clicky.FormatOptions{YAML: true}),
		Entry("JSON with Markdown artifact", clicky.FormatOptions{Format: "json,markdown=report.md"}),
		Entry("file output", clicky.FormatOptions{Format: "pretty", Output: "report.txt"}),
	)

	DescribeTable("appends locations to the generated list and its root alias only after success", func(child bool, runErr error) {
		previous := clicky.Flags.FormatOptions
		DeferCleanup(func() { clicky.Flags.FormatOptions = previous })
		clicky.Flags.FormatOptions = clicky.FormatOptions{NoColor: true}
		root := &cobra.Command{Use: "captain"}
		var out bytes.Buffer
		root.SetOut(&out)
		run := func(cmd *cobra.Command, _ []string) error {
			cmd.Print("prompt rows\n")
			return runErr
		}
		prompt := &cobra.Command{Use: "prompt", RunE: run}
		list := &cobra.Command{Use: "list", RunE: run}
		prompt.AddCommand(list)
		root.AddCommand(prompt)
		Expect(AttachPromptListOutput(root)).To(Succeed())
		target := prompt
		if child {
			target = list
		}
		// Cobra sets the context only on Execute; a direct RunE call must supply it.
		target.SetContext(context.Background())
		err := target.RunE(target, nil)
		if runErr == nil {
			Expect(err).NotTo(HaveOccurred())
			Expect(out.String()).To(MatchRegexp("(?s)prompt rows.*Lookup locations"))
		} else {
			Expect(err).To(MatchError(runErr))
			Expect(out.String()).To(Equal("prompt rows\n"))
		}
	},
		Entry("list command", true, nil),
		Entry("root alias", false, nil),
		Entry("failed list", true, errors.New("cannot list prompts")),
	)
})
