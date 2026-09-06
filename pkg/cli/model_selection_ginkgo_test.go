package cli

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CLI model selection", func() {
	BeforeEach(func() {
		path := filepath.Join(GinkgoT().TempDir(), ".captain.yaml")
		Expect(os.WriteFile(path, []byte("ai:\n  defaultProvider: anthropic\n  providers:\n    anthropic:\n      model: opus\n      mode: agent\n"), 0o600)).To(Succeed())
		captainconfig.SetPathForTesting(path)
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
	})

	It("does not attach the saved Claude runtime to an explicit model", func() {
		opts := AIPromptOptions{}
		opts.Model = "gemini-3.5-flash"

		req, cfg, err := runtimeLayersForTest(ai.Request{}, opts)

		Expect(err).NotTo(HaveOccurred())
		Expect(req.Model.Name).To(Equal("gemini-3.5-flash"))
		Expect(req.Model.Provider).To(Equal(api.Google))
		Expect(req.Model.Mode).To(Equal(api.ModeAPI))
		Expect(cfg.Model).To(Equal(req.Model))
	})

	It("uses the same precedence for provider options", func() {
		cfg, err := providerConfigForTest(AIProviderOptions{ModelFlags: aiflags.ModelFlags{Model: "gemini-3.5-flash"}})

		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Model.Name).To(Equal("gemini-3.5-flash"))
		Expect(cfg.Model.Provider).To(Equal(api.Google))
		Expect(cfg.Model.Mode).To(Equal(api.ModeAPI))
	})

	It("reports effort normalization while preserving the authored selector", func() {
		// The compact grammar is mode:model[:effort]; the prefix is never an adapter.
		resolved, err := resolveInvocation(AIRuntimeOptions{AIProviderOptions: AIProviderOptions{ModelFlags: aiflags.ModelFlags{Model: "api:gemini-3.6-flash:xhigh"}}}, nil)

		Expect(err).NotTo(HaveOccurred())
		cfg := resolved.Config
		Expect(cfg.Model.Name).To(Equal("gemini-3.6-flash"))
		Expect(cfg.Model.Provider).To(Equal(api.Google))
		Expect(cfg.Model.Mode).To(Equal(api.ModeAPI))
		Expect(cfg.Model.Effort).To(Equal(api.EffortHigh))
		Expect(resolved.Resolution.Provenance["/effort"].Source.Name).To(Equal("CLI flags"))
		Expect(resolved.Resolution.Provenance["/effort"].NormalizedBy).To(HaveField("Kind", api.FieldSourceCatalog))
		Expect(resolved.Resolution.Trace).To(HaveExactElements(HaveField("Spec.Model.Name", "api:gemini-3.6-flash:xhigh")))
	})

	It("accepts the whoami catalog model when configuring Gemini cli defaults", func() {
		Expect(validateProviderDefaults(context.Background(), api.Google, ProviderDefaultView{
			Mode: "cli", Model: "gemini-3.6-flash", Effort: "high",
		})).To(Succeed())
	})

	It("keeps the saved model and runtime paired when there is no override", func() {
		cfg, err := providerConfigForTest(AIProviderOptions{})

		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Model.Name).To(Equal("claude-opus-5"))
		Expect(cfg.Model.Provider).To(Equal(api.Anthropic))
		Expect(cfg.Model.Mode).To(Equal(api.ModeAgent))
	})

	layeredModel := func(frontmatter, request api.Model) api.Model {
		GinkgoHelper()
		layers, err := promptLayers(nil, "selection.prompt", ai.Request{Model: frontmatter}, &api.Spec{Model: request})
		Expect(err).NotTo(HaveOccurred())
		resolved, err := resolveInvocation(AIRuntimeOptions{}, layers)
		Expect(err).NotTo(HaveOccurred())
		return resolved.Request.Model
	}
	frontmatterModel := api.Model{Name: "claude-opus-4-6", Mode: api.ModeAPI, Provider: api.Anthropic}

	It("keeps the frontmatter mode for a bare request-layer rename and re-derives the provider", func() {
		resolved := layeredModel(frontmatterModel, api.Model{Name: "gemini-3.5-flash"})

		Expect(resolved.Name).To(Equal("gemini-3.5-flash"))
		Expect(resolved.Provider).To(Equal(api.Google))
		Expect(resolved.Mode).To(Equal(api.ModeAPI))
	})

	It("lets a mode-prefixed request-layer selector override the frontmatter mode", func() {
		resolved := layeredModel(frontmatterModel, api.Model{Name: "cli:gemini-3.5-flash"})

		Expect(resolved.Name).To(Equal("gemini-3.5-flash"))
		Expect(resolved.Provider).To(Equal(api.Google))
		Expect(resolved.Mode).To(Equal(api.ModeCLI))
	})

	It("rejects a malformed request-layer selector before layering", func() {
		_, err := promptLayers(nil, "selection.prompt", ai.Request{Model: frontmatterModel}, &api.Spec{Model: api.Model{Name: "warp:gemini-3.5-flash"}})

		Expect(err).To(MatchError(ContainSubstring(`spec layer "render request": model:`)))
	})

	It("omits single-runtime identity from multi-model parent labels", func() {
		labels := promptTaskLabels(PromptRenderResult{
			Name: "test", Model: "opus", Provider: api.Anthropic.Name, Mode: string(api.ModeAgent),
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
			return AIPromptResult{Text: "ok", Model: cfg.Model.Name, Provider: cfg.Model.Provider.Name, Mode: string(cfg.Model.Mode)}, nil
		}

		opts := AIPromptOptions{MultiModels: []string{"gemini-3.5-flash"}}
		rendered, err := testRenderedVariants(testRenderedPrompt(api.Model{Name: "opus"}).Input, opts)
		Expect(err).NotTo(HaveOccurred())
		result, err := executeSyncBatch(context.Background(), rendered, opts)

		Expect(err).NotTo(HaveOccurred())
		Expect(executed.Model.Name).To(Equal("gemini-3.5-flash"))
		Expect(executed.Model.Provider).To(Equal(api.Google))
		Expect(executed.Model.Mode).To(Equal(api.ModeAPI))
		Expect(result.Runs).To(HaveLen(1))
		// The selector is the compact form the user could have typed: a mode prefix,
		// never a provider name.
		Expect(result.Runs[0].Selector).To(Equal("api:gemini-3.5-flash"))
	})

	It("replaces the configured runtime identity for a multi-model variant", func() {
		configured := withCaps(api.Model{Name: "gpt-5.6-luna", Mode: api.ModeCLI})
		selected := withCaps(api.Model{Name: "gemini-3.5-flash", Mode: api.ModeAPI, Effort: api.EffortHigh})

		variant := renderVariant(testRenderedPrompt(configured), testRuntimeVariant(selected)).Config.Model

		Expect(variant.Validate()).To(Succeed())
		Expect(variant).To(Equal(selected))
	})

	It("preserves runtime fallbacks when no CLI fallback override is set", func() {
		selected := api.Model{
			Name:      "gemini-3.5-flash",
			Provider:  api.Google,
			Mode:      api.ModeAPI,
			Fallbacks: api.ModelList{{Name: "gemini-3-flash", Mode: api.ModeAPI}},
		}

		variant := renderVariant(testRenderedPrompt(api.Model{}), testRuntimeVariant(selected)).Config.Model

		Expect(variant.Fallbacks).To(Equal(selected.Fallbacks))
	})
})
