package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prompt-declared runtimes", func() {
	var path string

	BeforeEach(func() {
		configPath := filepath.Join(GinkgoT().TempDir(), ".captain.yaml")
		captainconfig.SetPathForTesting(configPath)
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		path = filepath.Join(GinkgoT().TempDir(), "compare.prompt")
		Expect(os.WriteFile(path, []byte(`---
name: Compare UI audits
model: gemini-3.5-flash
runtimes:
  - api:gemini-3.5-flash:high
  - model: claude-sonnet-5
    mode: api
    effort: medium
---
{{role "user"}}
Review the screenshot.
`), 0o600)).To(Succeed())
	})

	It("renders root runtimes as resolved parallel defaults", func() {
		rendered, err := renderPromptCLI(context.Background(), path, AIPromptOptions{}, "", "")

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes).To(HaveLen(2))
		Expect(rendered.Runtimes[0]).To(SatisfyAll(
			HaveField("Name", "gemini-3.5-flash"),
			HaveField("Provider", api.Google), HaveField("Mode", api.ModeAPI),
			HaveField("Effort", api.EffortHigh),
		))
		Expect(rendered.Runtimes[1]).To(SatisfyAll(
			HaveField("Name", "claude-sonnet-5"),
			HaveField("Provider", api.Anthropic), HaveField("Mode", api.ModeAPI),
			HaveField("Effort", api.EffortMedium),
		))
	})

	It("serves the canonical prompt run request with the detail", func() {
		record, err := filePromptRecord(path)
		Expect(err).NotTo(HaveOccurred())

		detail, err := promptDetail(record)

		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Run).To(Equal(PromptRenderRequest{
			Variables: map[string]any{},
			Spec: &api.Spec{Model: api.Model{
				Name: "gemini-3.5-flash",
				Mode: api.ModeAPI,
			}},
			Runtimes: []api.Model{
				{
					Name:   "gemini-3.5-flash",
					Mode:   api.ModeAPI,
					Effort: api.EffortHigh,
				},
				{
					Name:   "claude-sonnet-5",
					Mode:   api.ModeAPI,
					Effort: api.EffortMedium,
				},
			},
			Chat: true,
		}))
	})

	DescribeTable("resolves a discovered prompt by bare filename",
		func(id string) {
			ctx := ContextWithPromptDirs(context.Background(), []string{filepath.Dir(path)})
			expectedPath, err := filepath.EvalSymlinks(path)
			Expect(err).NotTo(HaveOccurred())

			record, err := resolvePromptRecord(ctx, id)

			Expect(err).NotTo(HaveOccurred())
			Expect(record.Path).To(Equal(expectedPath))
			Expect(record.Rel).To(Equal("compare.prompt"))
		},
		Entry("without the extension", "compare"),
		Entry("with the extension", "compare.prompt"),
	)

	It("rejects an ambiguous bare filename", func() {
		otherDir := GinkgoT().TempDir()
		otherPath := filepath.Join(otherDir, filepath.Base(path))
		Expect(os.WriteFile(otherPath, []byte("Review this screenshot."), 0o600)).To(Succeed())
		ctx := ContextWithPromptDirs(context.Background(), []string{filepath.Dir(path), otherDir})

		_, err := resolvePromptRecord(ctx, "compare")

		Expect(err).To(MatchError(ContainSubstring("prompt name \"compare\" is ambiguous")))
	})

	It("does not require a singular base model", func() {
		content, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(path, []byte(strings.Replace(string(content), "model: gemini-3.5-flash\n", "", 1)), 0o600)).To(Succeed())

		rendered, err := renderPromptCLI(context.Background(), path, AIPromptOptions{}, "", "")

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Runtimes).To(HaveLen(2))
	})

	It("lets explicit CLI runtimes replace prompt defaults", func() {
		rendered, err := renderPromptCLI(context.Background(), path, AIPromptOptions{
			MultiModels: []string{"api:gpt-5.5:high,agent:sol:medium"},
		}, "", "")

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes).To(HaveLen(2))
		Expect(rendered.Runtimes[0].Name).To(Equal("gpt-5.5"))
		Expect(rendered.Runtimes[1].Provider).To(Equal(api.OpenAI))
		Expect(rendered.Runtimes[1].Mode).To(Equal(api.ModeAgent))
	})

	It("lets explicit HTTP runtimes replace prompt defaults", func() {
		record, err := filePromptRecord(path)
		Expect(err).NotTo(HaveOccurred())
		rendered, err := renderPrompt(context.Background(), record.ID, PromptRenderRequest{Runtimes: []api.Model{
			{Name: "gpt-5.5", Mode: api.ModeAPI, Effort: api.EffortHigh},
			{Name: "gpt-5.6-sol", Mode: api.ModeAgent, Effort: api.EffortMedium},
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes).To(HaveLen(2))
		Expect(rendered.Runtimes[0].Provider).To(Equal(api.OpenAI))
		Expect(rendered.Runtimes[0].Mode).To(Equal(api.ModeAPI))
		Expect(rendered.Runtimes[1].Provider).To(Equal(api.OpenAI))
		Expect(rendered.Runtimes[1].Mode).To(Equal(api.ModeAgent))
	})

	DescribeTable("executes name-resolved runtimes without CLI selectors",
		func(id string) {
			withGinkgoCaptainDB()

			originalExecute := executePromptRequestFunc
			DeferCleanup(func() { executePromptRequestFunc = originalExecute })
			var mu sync.Mutex
			executed := []api.Model{}
			executePromptRequestFunc = func(_ context.Context, _ ai.Request, cfg ai.Config, _ time.Duration, _ bool) (any, error) {
				mu.Lock()
				defer mu.Unlock()
				executed = append(executed, cfg.Model)
				return AIPromptResult{Text: "ok", Model: cfg.Model.Name, Provider: cfg.Model.Provider.Name, Mode: string(cfg.Model.Mode)}, nil
			}
			ctx := ContextWithPromptDirs(context.Background(), []string{filepath.Dir(path)})
			rendered, err := renderPromptCLI(ctx, id, AIPromptOptions{}, "", "")
			Expect(err).NotTo(HaveOccurred())

			result, err := executeSyncRun(ctx, rendered, AIPromptOptions{})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Runs).To(HaveLen(2))
			Expect(executed).To(ConsistOf(
				SatisfyAll(HaveField("Name", "gemini-3.5-flash"), HaveField("Provider", api.Google), HaveField("Mode", api.ModeAPI)),
				SatisfyAll(HaveField("Name", "claude-sonnet-5"), HaveField("Provider", api.Anthropic), HaveField("Mode", api.ModeAPI)),
			))
		},
		Entry("without the extension", "compare"),
		Entry("with the extension", "compare.prompt"),
	)

	It("rejects unknown fields in prompt runtimes", func() {
		invalidPath := filepath.Join(GinkgoT().TempDir(), "invalid.prompt")
		Expect(os.WriteFile(invalidPath, []byte(`---
runtimes:
  - model: gemini-3.5-flash
    mode: api
    typo: true
  - api:sonnet-5:medium
---
Review the screenshot.
`), 0o600)).To(Succeed())

		_, err := renderPromptCLI(context.Background(), invalidPath, AIPromptOptions{}, "", "")

		Expect(err).To(MatchError(ContainSubstring("field typo not found")))
	})
})

var _ = Describe("prompt schema model catalog", func() {
	It("serves one row per model, listing the modes that can execute it", func() {
		models := PromptModelCatalog([]AdapterStatus{
			{
				Provider:      api.OpenAI.Name,
				Mode:          string(api.ModeCLI),
				Type:          "cli",
				Authenticated: true,
				Binary:        "/usr/local/bin/codex",
				Models:        []string{"gpt-5.6-sol"},
			},
			{
				Provider:      api.OpenAI.Name,
				Mode:          string(api.ModeCmux),
				Type:          "cli",
				Authenticated: true,
				Binary:        "/usr/local/bin/codex",
				Models:        []string{"gpt-5.6-sol"},
			},
		})

		Expect(models).To(HaveLen(1))
		Expect(models[0].Provider).To(Equal("openai"))
		Expect(models[0].Modes).To(Equal([]string{"cli", "cmux"}))
		Expect(models[0].Runtime).To(Equal(api.Model{Name: "gpt-5.6-sol"}))
	})
})
