package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/flanksource/clicky"
	"github.com/flanksource/gavel/linters"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Promptfile lint command", func() {
	It("registers a local-only command with sample-input flags", func() {
		command, _, err := newRootCommand().Find([]string{"lint", "promptfile"})
		Expect(err).NotTo(HaveOccurred())
		Expect(command.Name()).To(Equal("promptfile"))
		Expect(clicky.IsLocalOnly(command)).To(BeTrue())
		Expect(command.Flags().Lookup("var")).NotTo(BeNil())
		Expect(command.Flags().Lookup("vars")).NotTo(BeNil())
	})

	DescribeTable("emits Gavel JSON and applies the framework severity verdict",
		func(source string, extra []string, fails bool, findings int) {
			path := filepath.Join(GinkgoT().TempDir(), "review.prompt")
			Expect(os.WriteFile(path, []byte(source), 0o600)).To(Succeed())
			output, err := capturePromptfileOutput(append([]string{"lint", "promptfile", path, "--format", "json"}, extra...))
			Expect(err != nil).To(Equal(fails), output)
			var results []*linters.LinterResult
			Expect(json.Unmarshal([]byte(output), &results)).To(Succeed(), output)
			Expect(results).To(HaveLen(1))
			Expect(results[0].Linter).To(Equal("promptfile"))
			Expect(results[0].FileCount).To(Equal(1))
			Expect(results[0].Violations).To(HaveLen(findings))
		},
		Entry("valid reusable prompt", "Review {{missing}}\n", nil, false, 0),
		Entry("invalid YAML retains its finding", "---\nmodel: [unfinished\n---\nbody\n", nil, true, 1),
		Entry("missing inputs warn", "---\nbudget:\n  maxTokens: {{limit}}\n---\nbody\n", nil, false, 1),
		Entry("JSON sample inputs", "---\nbudget:\n  maxTokens: {{limit}}\n---\nbody\n", []string{"--vars", `{"limit": 100}`}, false, 0),
		Entry("individual inputs override JSON", "---\nbudget:\n  maxTokens: {{limit}}\n---\nbody\n", []string{"--vars", `{"limit": -1}`, "--var", "limit=100"}, false, 0),
		Entry("rendered invalid option", "---\nbudget:\n  maxTokens: {{limit}}\n---\nbody\n", []string{"--var", "limit=-1"}, true, 1),
	)

	DescribeTable("preserves structured failures for invalid inputs",
		func(extra []string, message string) {
			output, err := capturePromptfileOutput(append([]string{"lint", "promptfile", "--format", "json"}, extra...))
			Expect(err).To(HaveOccurred())
			var results []*linters.LinterResult
			Expect(json.Unmarshal([]byte(output), &results)).To(Succeed(), output)
			Expect(results).To(ConsistOf(And(HaveField("Success", false), HaveField("Error", ContainSubstring(message)))))
		},
		Entry("missing file", []string{"missing-review.prompt"}, "missing-review.prompt"),
		Entry("malformed JSON", []string{"--vars", "["}, "--vars"),
		Entry("null JSON", []string{"--vars", "null", "--var", "limit=100"}, "object"),
		Entry("malformed variable", []string{"--var", "limit"}, "key=value"),
	)

	It("renders actionable diagnostics in the human format", func() {
		path := filepath.Join(GinkgoT().TempDir(), "review.prompt")
		Expect(os.WriteFile(path, []byte("---\nmode: invalid\n---\nbody\n"), 0o600)).To(Succeed())
		output, err := capturePromptfileOutput([]string{"lint", "promptfile", path, "--format", "pretty", "--no-color"})
		Expect(err).To(HaveOccurred())
		Expect(output).To(ContainSubstring("review.prompt"))
		Expect(output).To(ContainSubstring("mode"))
		Expect(output).To(ContainSubstring("Prompt lint"))
		Expect(output).To(ContainSubstring("1 error"))
		Expect(output).To(ContainSubstring("0 warnings"))
	})

	It("distinguishes warning-only lint results in the human format", func() {
		path := filepath.Join(GinkgoT().TempDir(), "review.prompt")
		Expect(os.WriteFile(path, []byte("---\nmodel: {{model}}\n---\nReview.\n"), 0o600)).To(Succeed())
		output, err := capturePromptfileOutput([]string{"lint", "promptfile", path, "--format", "pretty", "--no-color"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("0 errors"))
		Expect(output).To(ContainSubstring("1 warning"))
		Expect(output).To(ContainSubstring("review.prompt"))
	})

	It("keeps the broken-link cause visible in human output", func() {
		directory := GinkgoT().TempDir()
		path := filepath.Join(directory, "broken.prompt")
		Expect(os.Symlink(filepath.Join(directory, "missing.prompt"), path)).To(Succeed())
		output, err := capturePromptfileOutput([]string{"lint", "promptfile", path, "--format", "pretty", "--no-color"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(ContainSubstring("1 warning"))
		Expect(output).To(ContainSubstring("no such file or directory"))
		Expect(output).To(ContainSubstring("promptfile/symlink"))
	})
})

func capturePromptfileOutput(args []string) (string, error) {
	GinkgoHelper()
	output, err := os.CreateTemp(GinkgoT().TempDir(), "stdout")
	Expect(err).NotTo(HaveOccurred())
	previousStdout, previousFlags := os.Stdout, *clicky.Flags
	defer func() {
		os.Stdout, *clicky.Flags = previousStdout, previousFlags
		Expect(output.Close()).To(Succeed())
	}()
	os.Stdout = output
	root := newRootCommand()
	root.SetArgs(args)
	err = root.Execute()
	data, readErr := os.ReadFile(output.Name())
	Expect(readErr).NotTo(HaveOccurred())
	return string(data), err
}
