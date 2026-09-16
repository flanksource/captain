package cli

import (
	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net/http"
	"net/http/httptest"
)

var _ = Describe("Composed prompt preset layers", func() {
	It("allows the complete render request to repair a preset runtime", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.preset(runtimeprofiles.PresetInput{Name: "Restricted", Scope: api.SpecLayerSurface, Spec: api.RuntimePresetSpec(api.Spec{
			Model:       api.Model{Name: "gpt-5.6-sol", Mode: api.ModeCLI},
			Permissions: api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyDeny}},
		})})
		presets, warnings, err := selectRuntimePresets(f.ctx, runtimePresetSelection{Requested: []string{"restricted"}, RequestedSet: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
		Expect(presets.Resolved).To(Equal(api.ResolvedSpec{}))
		Expect(presets.Layers).To(HaveLen(1))
		layers, err := promptLayers(presets, "review.prompt", api.Spec{}, &api.Spec{
			Model: api.Model{Name: "agent:claude-sonnet-5"},
		})
		Expect(err).NotTo(HaveOccurred())
		resolved, err := api.ResolveSpecLayers(api.ResolveSpecOptions{Layers: layers})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Mode).To(Equal(api.ModeAgent))
		Expect(resolved.Spec.Permissions.Tools).To(Equal(api.Tools{"Bash": api.ToolPolicyDeny}))
		Expect(traceNames(resolved)).To(Equal([]string{"Restricted", "review.prompt", "render request"}))
	})

	It("keeps an incomplete chat preset available for a later request", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.preset(runtimeprofiles.PresetInput{Name: "Restricted", Scope: api.SpecLayerSurface, Spec: api.RuntimePresetSpec(api.Spec{
			Model:       api.Model{Name: "gpt-5.6-sol", Mode: api.ModeCLI},
			Permissions: api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyDeny}},
		})})
		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx, aichat.WithRuntimePresets([]string{"restricted"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Spec.Name).To(Equal("gpt-5.6-sol"))
		Expect(profile.Composed.Spec.Provider).To(BeNil())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve"), HaveField("Name", "Restricted")))
	})

	DescribeTable("rejects malformed request fields hidden by a compact model", func(model api.Model) {
		_, err := promptLayers(nil, "review.prompt", api.Spec{}, &api.Spec{Model: model})
		Expect(err).To(HaveOccurred())
	},
		Entry("invalid mode", api.Model{Name: "agent:sonnet:high", Mode: "invalid"}),
		Entry("invalid effort", api.Model{Name: "agent:sonnet:high", Effort: "invalid"}),
	)

	It("preserves a compact render request until the shared final fold", func() {
		request := api.Spec{Model: api.Model{Name: "agent:sonnet:high"}}
		layers, err := promptLayers(nil, "review.prompt", api.Spec{}, &request)
		Expect(err).NotTo(HaveOccurred())
		Expect(layers[1].Spec).To(Equal(request))
	})

	It("reports a missing selected preset as a request error", func() {
		f, _, _ := newRuntimeCatalogFixture()
		service := aichat.NewService(aichat.ServiceOptions{Profile: captainChatProfileProvider(GinkgoT().TempDir())})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/models?preset=missing", nil).WithContext(f.ctx))
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("missing"))
	})
})
