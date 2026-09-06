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

var _ = Describe("chat runtime profile provider", func() {
	// chatCatalog seeds a context-scoped "Team" preset under a "Review" profile
	// and a preset-less "Plan" profile.
	chatCatalog := func() runtimeCatalogFixture {
		GinkgoHelper()
		f, _, _ := newRuntimeCatalogFixture()
		f.preset(runtimeprofiles.PresetInput{
			Name: "Team", Scope: api.SpecLayerContext,
			Spec: api.RuntimePresetSpec{
				Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAgent}, Budget: api.Budget{MaxTurns: 20},
			},
		})
		f.profile(runtimeprofiles.ProfileInput{
			Name: "Review", Presets: []string{"team"}, Spec: api.Spec{Budget: api.Budget{MaxTurns: 5}},
		})
		f.profile(runtimeprofiles.ProfileInput{
			Name: "Plan", Spec: api.Spec{Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAgent}},
		})
		return f
	}
	saveChatDefault := func(ref string) {
		GinkgoHelper()
		Expect(captainconfig.Save(captainconfig.Config{Chat: captainconfig.ChatDefaults{RuntimeProfile: ref}})).To(Succeed())
	}

	It("layers the served base, the presets and the profile spec in scope order", func() {
		f := chatCatalog()
		cwd := GinkgoT().TempDir()

		profile, err := captainChatProfileProvider(cwd).RuntimeProfile(f.ctx, aichat.WithRuntimeProfileRef("review"))

		Expect(err).NotTo(HaveOccurred())
		Expect(profile.System).To(Equal(captainChatSystemPrompt))
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve"), HaveField("Name", "Team"), HaveField("Name", "Review run spec")))
		Expect(profile.Composed.Trace).To(HaveExactElements(
			HaveField("Scope", api.SpecLayerGlobal),
			HaveField("Scope", api.SpecLayerContext),
			HaveField("Scope", api.SpecLayerSurface),
		))
		Expect(profile.Composed.Spec.Model.Name).To(Equal("claude-sonnet-4-6"), "the preset overrides the base model")
		Expect(profile.Composed.Spec.Budget.MaxTurns).To(Equal(5), "the profile spec overrides the preset")
		Expect(profile.Composed.Spec.Cwd()).To(Equal(cwd), "the base layer survives")
	})

	It("serves the base layer alone when nothing selects a profile", func() {
		f := chatCatalog()

		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx)

		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve")))
		Expect(profile.Composed.Spec.Model.Name).To(Equal("sol"))
	})

	It("applies the configured chat default when the request names no profile", func() {
		f := chatCatalog()
		saveChatDefault("review")

		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx)

		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve"), HaveField("Name", "Team"), HaveField("Name", "Review run spec")))
	})

	It("lets the request's profile override the configured default", func() {
		f := chatCatalog()
		saveChatDefault("review")

		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx, aichat.WithRuntimeProfileRef("plan"))

		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve"), HaveField("Name", "Plan run spec")))
	})

	It("rejects an unknown request profile as a 400 and a broken default as a server error", func() {
		f := chatCatalog()
		service := aichat.NewService(aichat.ServiceOptions{Profile: captainChatProfileProvider(GinkgoT().TempDir())})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/models?runtimeProfile=nope", nil).WithContext(f.ctx))
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(`runtime profile "nope"`))

		saveChatDefault("ghost")
		response = httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/models", nil).WithContext(f.ctx))
		Expect(response.Code).To(Equal(http.StatusInternalServerError), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(`runtime profile "ghost"`))
	})
})
