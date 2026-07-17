package cli

import (
	"context"
	"github.com/flanksource/captain/pkg/aiflags"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

var _ = Describe("CLI model selection", func() {
	BeforeEach(func() {
		path := filepath.Join(GinkgoT().TempDir(), ".captain.yaml")
		Expect(os.WriteFile(path, []byte("ai:\n  model: opus\n  backend: claude-agent\n"), 0o600)).To(Succeed())
		captainconfig.SetPathForTesting(path)
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
	})

	It("does not attach the saved Claude backend to an explicit model", func() {
		opts := AIPromptOptions{}
		opts.Model = "gemini-3.5-flash"

		req, cfg, err := overlayCLI(ai.Request{}, ai.Config{}, opts)

		Expect(err).NotTo(HaveOccurred())
		Expect(req.Model.Name).To(Equal("gemini-3.5-flash"))
		Expect(req.Model.Backend).To(Equal(api.BackendGemini))
		Expect(cfg.Model).To(Equal(req.Model))
	})

	It("uses the same precedence for provider options", func() {
		cfg, err := (AIProviderOptions{ModelFlags: aiflags.ModelFlags{Model: "gemini-3.5-flash"}}).ToConfig()

		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Model.Name).To(Equal("gemini-3.5-flash"))
		Expect(cfg.Model.Backend).To(Equal(api.BackendGemini))
	})

	It("keeps the saved model and backend paired when there is no override", func() {
		cfg, err := (AIProviderOptions{}).ToConfig()

		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Model.Name).To(Equal("claude-opus-4-8"))
		Expect(cfg.Model.Backend).To(Equal(api.BackendClaudeAgent))
	})

	It("does not retain a prompt backend when a structured spec replaces its model", func() {
		req := ai.Request{Model: api.Model{Name: "opus", Backend: api.BackendClaudeAgent}}
		cfg := ai.Config{Model: req.Model}

		overlayRuntimeSpec(&req, &cfg, api.Spec{Model: api.Model{Name: "gemini-3.5-flash"}})
		Expect(applyPromptDefaults(&req, &cfg)).To(Succeed())
		resolved, err := ai.ResolveModelSelectors(cfg.Model)

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Name).To(Equal("gemini-3.5-flash"))
		Expect(resolved.Backend).To(Equal(api.BackendGemini))
	})

	It("omits single-runtime identity from multi-model parent labels", func() {
		labels := promptTaskLabels(PromptRenderResult{
			Name: "test", Model: "opus", Backend: string(api.BackendClaudeAgent),
		}, "multi")

		Expect(labels).To(HaveKeyWithValue("mode", "multi"))
		Expect(labels).NotTo(HaveKey("model"))
		Expect(labels).NotTo(HaveKey("backend"))
	})

	It("passes a concrete Gemini runtime to a bare multi-model execution", func() {
		originalExecute := executePromptRequestFunc
		DeferCleanup(func() { executePromptRequestFunc = originalExecute })
		var executed ai.Config
		executePromptRequestFunc = func(_ context.Context, _ ai.Request, cfg ai.Config, _ time.Duration, _ bool) (any, error) {
			executed = cfg
			return AIPromptResult{Text: "ok", Model: cfg.Model.Name, Backend: string(cfg.Model.Backend)}, nil
		}

		result, err := executeSyncBatch(
			context.Background(),
			testRenderedPrompt(api.Model{Name: "opus", Backend: api.BackendClaudeAgent}),
			AIPromptOptions{AIRuntimeOptions: AIRuntimeOptions{AIProviderOptions: AIProviderOptions{}}, MultiModels: []string{"gemini-3.5-flash"}},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(executed.Model.Name).To(Equal("gemini-3.5-flash"))
		Expect(executed.Model.Backend).To(Equal(api.BackendGemini))
		Expect(result.Runs).To(HaveLen(1))
		Expect(result.Runs[0].Selector).To(Equal("gemini:gemini-3.5-flash"))
	})
})
