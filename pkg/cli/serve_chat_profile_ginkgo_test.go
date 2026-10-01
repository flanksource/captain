package cli

import (
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("chat runtime preset provider", func() {
	// chatCatalog seeds independently selectable context and surface presets.
	chatCatalog := func() runtimeCatalogFixture {
		GinkgoHelper()
		f, _, _ := newRuntimeCatalogFixture()
		f.preset(runtimeprofiles.PresetInput{
			Name: "Team", Scope: api.SpecLayerContext,
			Spec: api.RuntimePresetSpec{
				Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAgent}, Budget: api.Budget{MaxTurns: 20},
			},
		})
		f.preset(runtimeprofiles.PresetInput{
			Name: "Review", Scope: api.SpecLayerSurface,
			Spec: api.RuntimePresetSpec(api.Spec{Budget: api.Budget{MaxTurns: 5}}),
		})
		f.preset(runtimeprofiles.PresetInput{
			Name: "Plan", Scope: api.SpecLayerSurface,
			Spec: api.RuntimePresetSpec(api.Spec{Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAgent}}),
		})
		return f
	}
	saveChatDefault := func(refs ...string) {
		GinkgoHelper()
		Expect(captainconfig.Save(captainconfig.Config{Chat: captainconfig.ChatDefaults{Presets: refs}})).To(Succeed())
	}

	It("layers the served base and selected presets in scope order", func() {
		f := chatCatalog()
		cwd := GinkgoT().TempDir()

		profile, err := captainChatProfileProvider(cwd).RuntimeProfile(f.ctx, aichat.WithRuntimePresets([]string{"team", "review"}))

		Expect(err).NotTo(HaveOccurred())
		Expect(profile.System).To(Equal(captainChatSystemPrompt))
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve"), HaveField("Name", "Team"), HaveField("Name", "Review")))
		Expect(profile.Composed.Trace).To(HaveExactElements(
			HaveField("Scope", api.SpecLayerGlobal),
			HaveField("Scope", api.SpecLayerContext),
			HaveField("Scope", api.SpecLayerSurface),
		))
		Expect(profile.Composed.Spec.Model.Name).To(Equal("claude-sonnet-4-6"), "the preset overrides the base model")
		Expect(profile.Composed.Spec.Budget.MaxTurns).To(Equal(5), "the surface preset overrides the context preset")
		Expect(profile.Composed.Spec.Cwd()).To(Equal(cwd), "the base layer survives")
	})

	It("serves a model-free base layer when nothing selects presets or a saved model", func() {
		f, _, _ := newRuntimeCatalogFixture()

		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx)

		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve")))
		Expect(profile.Composed.Spec.Model.Name).To(BeEmpty())
	})

	It("takes model settings from one saved snapshot while keeping them out of authored layers", func() {
		f, _, _ := newRuntimeCatalogFixture()
		Expect(captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{DefaultModel: "agent:sonnet:high", BudgetUSD: 4}})).To(Succeed())
		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Saved).NotTo(BeNil())
		Expect(profile.Saved.DefaultModel).To(Equal("agent:sonnet:high"))
		Expect(profile.Composed.Spec.Name).To(Equal("sonnet"))
		Expect(profile.Composed.Spec.Effort).To(Equal(api.EffortHigh))
		Expect(profile.Composed.Spec.Budget.Cost).To(Equal(float64(4)))
		Expect(profile.Composed.Trace[0].Spec.Name).To(BeEmpty())
		Expect(profile.Composed.Provenance["/model"].Source.Key).To(Equal("ai.defaultModel"))
		Expect(captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{DefaultModel: "api:sol"}})).To(Succeed())
		Expect(profile.Saved.DefaultModel).To(Equal("agent:sonnet:high"))
	})

	It("rejects malformed saved settings even when an explicit preset supplies a valid model", func() {
		f := chatCatalog()
		Expect(captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{Temperature: 3}})).To(Succeed())
		_, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx, aichat.WithRuntimePresets([]string{"plan"}))
		Expect(err).To(MatchError(ContainSubstring("ai.temperature")))
	})

	It("applies configured chat defaults when the request names no presets", func() {
		f := chatCatalog()
		saveChatDefault("team", "review")

		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx)

		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve"), HaveField("Name", "Team"), HaveField("Name", "Review")))
	})

	It("lets request presets override configured defaults", func() {
		f := chatCatalog()
		saveChatDefault("team", "review")

		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx, aichat.WithRuntimePresets([]string{"plan"}))

		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve"), HaveField("Name", "Plan")))
	})

	It("rejects an unknown request preset as a 400 and a broken default as a server error", func() {
		f := chatCatalog()
		service := aichat.NewService(aichat.ServiceOptions{Profile: captainChatProfileProvider(GinkgoT().TempDir())})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/models?preset=nope", nil).WithContext(f.ctx))
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(`runtime presets "nope"`))

		saveChatDefault("ghost")
		response = httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/models", nil).WithContext(f.ctx))
		Expect(response.Code).To(Equal(http.StatusInternalServerError), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(`runtime presets "ghost"`))
	})

	It("warns and ignores deprecated profile selections and defaults", func() {
		f := chatCatalog()
		Expect(captainconfig.Save(captainconfig.Config{Chat: captainconfig.ChatDefaults{RuntimeProfile: "review"}})).To(Succeed())

		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx, aichat.WithRuntimeProfileRef("plan"))
		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve")))
		Expect(profile.Composed.Warnings).To(Equal([]string{api.RuntimeProfileDeprecationWarning}))
	})
})
