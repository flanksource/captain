package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"

	"github.com/flanksource/captain/pkg/ai"
)

var _ = Describe("prompt run input files", func() {
	var (
		dir      string
		mu       sync.Mutex
		executed []ai.Request
		failOn   string
	)

	BeforeEach(func() {
		withGinkgoCaptainDB()
		dir = GinkgoT().TempDir()
		executed, failOn = nil, ""
		original := executePromptRequestFunc
		DeferCleanup(func() { executePromptRequestFunc = original })
		executePromptRequestFunc = func(_ context.Context, req ai.Request, cfg ai.Config, _ time.Duration, _ bool) (any, error) {
			mu.Lock()
			executed = append(executed, req)
			mu.Unlock()
			if failOn != "" && strings.Contains(req.Prompt.User, failOn) {
				return nil, errors.New("model refused " + failOn)
			}
			return AIPromptResult{Text: "answer for " + req.Prompt.User, Model: cfg.Model.Name, Provider: "deepseek", Mode: "api"}, nil
		}
	})

	writePrompt := func(frontmatter string) string {
		path := filepath.Join(dir, "read.prompt")
		Expect(os.WriteFile(path, []byte("---\nmodel: deepseek-reasoner\nmode: api\n"+frontmatter+"---\nRead {{#each files}}{{this}};{{/each}} one={{file}}\n"), 0o600)).To(Succeed())
		return path
	}
	inputs := func(names ...string) []string {
		paths := make([]string, len(names))
		for i, name := range names {
			paths[i] = filepath.Join(dir, name)
			Expect(os.WriteFile(paths[i], []byte(name), 0o600)).To(Succeed())
		}
		return paths
	}
	run := func(prompt string, flags map[string]string, files []string) (PromptRunResult, error) {
		opts, err := actionFlagsToOptions(flags)
		Expect(err).NotTo(HaveOccurred())
		opts.InputFiles = files
		return RunPrompt(context.Background(), PromptInvocation{Reference: prompt, Options: opts, Stdin: strings.NewReader("")})
	}
	users := func() []string {
		var out []string
		for _, req := range executed {
			out = append(out, req.Prompt.User)
		}
		return out
	}

	It("runs every file in one run without a batch, binding files and not file", func() {
		files := inputs("a.md", "b.md")

		result, err := run(writePrompt(""), nil, files)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Runs).To(BeEmpty())
		Expect(users()).To(Equal([]string{"Read " + files[0] + ";" + files[1] + "; one="}))
	})

	It("runs one file per run with --batch=1, binding file", func() {
		files := inputs("a.md", "b.md")

		result, err := run(writePrompt(""), map[string]string{"batch": "1"}, files)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(SatisfyAll(HaveField("Status", "completed"), HaveField("Total", 2), HaveField("Succeeded", 2)))
		Expect(result.Runs).To(HaveExactElements(HaveField("Inputs", []string{files[0]}), HaveField("Inputs", []string{files[1]})))
		Expect(users()).To(ConsistOf("Read "+files[0]+"; one="+files[0], "Read "+files[1]+"; one="+files[1]))
	})

	It("prints each batch's response as it completes, keeping every run in the result", func() {
		files := inputs("a.md", "b.md")
		streamed := gbytes.NewBuffer()
		previous := logger.GetOutput()
		logger.SetOutput(streamed)
		DeferCleanup(func() { logger.SetOutput(previous) })

		result, err := run(writePrompt("batch: 1\n"), nil, files)

		Expect(err).NotTo(HaveOccurred())
		output := string(streamed.Contents())
		first, second := strings.Index(output, "✓ a.md"), strings.Index(output, "✓ b.md")
		Expect(first).To(BeNumerically(">=", 0), output)
		Expect(second).To(BeNumerically(">", first), output)
		Expect(output).To(And(ContainSubstring("answer for Read "+files[0]), ContainSubstring("answer for Read "+files[1])))
		Expect(result.Runs).To(HaveLen(2))
	})

	It("binds files lists of batch.size, the flag overriding the frontmatter batch", func() {
		files := inputs("a.md", "b.md", "c.md")

		result, err := run(writePrompt("batch: 1\n"), map[string]string{"batch": "2", "batch-concurrency": "2"}, files)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Runs).To(HaveExactElements(HaveField("Inputs", files[:2]), HaveField("Inputs", files[2:])))
		Expect(users()).To(ConsistOf("Read "+files[0]+";"+files[1]+"; one=", "Read "+files[2]+"; one="+files[2]))
	})

	It("splits --attach entries into the batches and attaches each to its own run", func() {
		files := inputs("a.png", "b.png")

		result, err := run(writePrompt("batch: 1\n"), map[string]string{"attach": files[0] + "," + files[1]}, nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Total).To(Equal(2))
		Expect(executed).To(HaveLen(2))
		for _, req := range executed {
			Expect(req.Prompt.Attachments).To(HaveLen(1))
			Expect(req.Prompt.User).To(ContainSubstring("one=" + req.Prompt.Attachments[0].Path))
		}
	})

	It("keeps running the other batches when one fails, then fails the command", func() {
		files := inputs("a.md", "b.md")
		failOn = "a.md"

		result, err := run(writePrompt("batch: 1\n"), nil, files)

		var batchErr *InputBatchesError
		Expect(errors.As(err, &batchErr)).To(BeTrue(), "got %v", err)
		Expect(result).To(SatisfyAll(HaveField("Status", "partial"), HaveField("Failed", 1), HaveField("Succeeded", 1)))
		Expect(result.Runs[0]).To(SatisfyAll(HaveField("Status", "failed"), HaveField("Error", ContainSubstring("model refused a.md"))))
		Expect(result.Runs[1].Status).To(Equal("completed"))
	})

	DescribeTable("refuses input it cannot split",
		func(frontmatter string, flags map[string]string, files func() []string, want string) {
			_, err := run(writePrompt(frontmatter), flags, files())
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(executed).To(BeEmpty())
		},
		Entry("a var the input files bind", "", map[string]string{"var": "file=other.md"}, func() []string { return inputs("a.md") },
			`variable "file" is bound from the input files`),
		Entry("a batch over runtimes", "batch: 1\nruntimes: [api:deepseek-reasoner, api:deepseek-chat]\n", nil, func() []string { return inputs("a.md") },
			"batch cannot be combined with a multi-runtime run"),
		Entry("a directory", "", nil, func() []string { return []string{dir} }, "is not a regular file"),
		Entry("a missing file", "", nil, func() []string { return []string{filepath.Join(dir, "missing.md")} }, "missing.md"),
		Entry("a zero batch", "", map[string]string{"batch": "0"}, func() []string { return inputs("a.md") }, "batch.size must be at least 1"),
	)
})
