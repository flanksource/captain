package promptfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/gavel/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPromptfile(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Promptfile linter")
}

var _ = Describe("Promptfile linter", func() {
	var directory string
	BeforeEach(func() { directory = GinkgoT().TempDir() })

	write := func(path, content string) {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
	}

	It("discovers promptfiles recursively, respecting gitignore and duplicate paths", func() {
		Expect(os.Mkdir(filepath.Join(directory, ".git"), 0o755)).To(Succeed())
		write(filepath.Join(directory, ".gitignore"), "ignored/\n")
		write(filepath.Join(directory, "a.prompt"), "Review {{missing}}.\n")
		write(filepath.Join(directory, "nested", "b.prompt"), "---\nmode: invalid\n---\nbody\n")
		write(filepath.Join(directory, "ignored", "bad.prompt"), "{{#if broken}}\n")
		write(filepath.Join(directory, "readme.md"), "{{#if broken}}\n")
		results := Run(context.Background(), Options{WorkDir: directory, Paths: []string{".", "a.prompt"}})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Linter).To(Equal("promptfile"))
		Expect(results[0].Success).To(BeTrue())
		Expect(results[0].FileCount).To(Equal(2))
		Expect(results[0].Violations).To(HaveLen(1))
		Expect(results[0].Violations[0].File).To(Equal("nested/b.prompt"))
		Expect(results[0].Test().Sum().Failed).To(Equal(1))
	})

	It("checks explicit ignored files", func() {
		Expect(os.Mkdir(filepath.Join(directory, ".git"), 0o755)).To(Succeed())
		write(filepath.Join(directory, ".gitignore"), "ignored.prompt\n")
		write(filepath.Join(directory, "ignored.prompt"), "{{#if broken}}\n")
		results := Run(context.Background(), Options{WorkDir: directory, Paths: []string{"ignored.prompt"}})
		Expect(results).To(HaveLen(1))
		Expect(results[0].FileCount).To(Equal(1))
		Expect(results[0].Test().Sum().Failed).To(Equal(1))
	})

	DescribeTable("reports broken prompt links as file warnings", func(explicit, cycle bool) {
		link := filepath.Join(directory, "broken.prompt")
		target := filepath.Join(directory, "missing.prompt")
		if cycle {
			target = link
		}
		Expect(os.Symlink(target, link)).To(Succeed())
		write(filepath.Join(directory, "healthy.prompt"), "Review.\n")
		var paths []string
		if explicit {
			paths = []string{"broken.prompt", "healthy.prompt"}
		}
		results := Run(context.Background(), Options{WorkDir: directory, Paths: paths})
		Expect(results[0].Success).To(BeTrue(), results[0].Error)
		Expect(results[0].FileCount).To(Equal(2))
		Expect(results[0].Violations).To(ConsistOf(And(
			HaveField("File", "broken.prompt"), HaveField("Severity", models.SeverityWarning),
			HaveField("Rule.Method", "promptfile/symlink"),
		)))
		Expect(results[0].Test().Sum().Failed).To(BeZero())
	}, Entry("discovered broken target", false, false), Entry("explicit broken target", true, false),
		Entry("discovered cycle", false, true), Entry("explicit cycle", true, true))

	It("defaults to the working directory and orders findings by path", func() {
		write(filepath.Join(directory, "z.prompt"), "{{#if broken}}\n")
		write(filepath.Join(directory, "a.prompt"), "{{#if broken}}\n")
		results := Run(context.Background(), Options{WorkDir: directory})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Violations).To(HaveLen(2))
		Expect(results[0].Violations[0].File).To(Equal("a.prompt"))
		Expect(results[0].Violations[1].File).To(Equal("z.prompt"))
	})

	DescribeTable("reports selection errors as failed linter results",
		func(paths []string, message string) {
			results := Run(context.Background(), Options{WorkDir: directory, Paths: paths})
			Expect(results).To(HaveLen(1))
			Expect(results[0].Success).To(BeFalse())
			Expect(results[0].Error).To(ContainSubstring(message))
			Expect(results[0].Test().Sum().Failed).To(Equal(1))
		},
		Entry("empty directory", nil, "no .prompt files"),
		Entry("missing path", []string{"missing.prompt"}, "missing.prompt"),
	)

	It("fails on explicit non-prompt files", func() {
		write(filepath.Join(directory, "notes.md"), "Review.\n")
		results := Run(context.Background(), Options{WorkDir: directory, Paths: []string{"notes.md"}})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Success).To(BeFalse())
		Expect(results[0].Error).To(ContainSubstring(".prompt"))
	})

	It("keeps unreadable ordinary files as failures", func() {
		path := filepath.Join(directory, "private.prompt")
		write(path, "Review.\n")
		Expect(os.Chmod(path, 0)).To(Succeed())
		DeferCleanup(os.Chmod, path, os.FileMode(0o600))
		results := Run(context.Background(), Options{WorkDir: directory})
		Expect(results[0].Success).To(BeFalse())
		Expect(results[0].Error).To(ContainSubstring("private.prompt"))
		Expect(results[0].Test().Sum().Failed).To(Equal(1))
	})

	It("keeps incomplete-input warnings non-failing and accepts sample inputs", func() {
		write(filepath.Join(directory, "review.prompt"), "---\nbudget:\n  maxTokens: {{limit}}\n---\nReview {{missing}}\n")
		results := Run(context.Background(), Options{WorkDir: directory})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Violations).To(HaveLen(1))
		Expect(results[0].Violations[0].Severity).To(Equal(models.SeverityWarning))
		Expect(results[0].Test().Sum().Failed).To(BeZero())
		results = Run(context.Background(), Options{WorkDir: directory, Data: map[string]any{"limit": 100}})
		Expect(results[0].Violations).To(BeEmpty())
		Expect(results[0].Success).To(BeTrue())
	})

	It("accepts the built-in prompts", func() {
		builtin, err := filepath.Abs(filepath.Join("..", "..", "ai", "prompt", "builtin"))
		Expect(err).NotTo(HaveOccurred())
		results := Run(context.Background(), Options{WorkDir: builtin, Data: map[string]any{"files": "docs/spec.md"}})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Success).To(BeTrue(), results[0].Error)
		Expect(results[0].FileCount).To(Equal(5))
		Expect(results[0].Violations).To(BeEmpty())
	})

	It("honors cancellation", func() {
		write(filepath.Join(directory, "review.prompt"), "Review.\n")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		results := Run(ctx, Options{WorkDir: directory})
		Expect(results).To(HaveLen(1))
		Expect(results[0].Success).To(BeFalse())
		Expect(results[0].Error).To(ContainSubstring("context canceled"))
	})
})
