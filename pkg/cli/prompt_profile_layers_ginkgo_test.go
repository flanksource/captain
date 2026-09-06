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

var _ = Describe("Composed prompt profile layers", func() {
	It("allows the complete render request to repair a profile runtime", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.profile(runtimeprofiles.ProfileInput{Name: "Restricted", Spec: api.Spec{
			Model:       api.Model{Name: "gpt-5.6-sol", Mode: api.ModeCLI},
			Permissions: api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyDeny}},
		}})
		profile, err := selectRuntimeProfile(f.ctx, runtimeProfileSelection{Requested: "restricted"})
		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Resolved).To(Equal(api.ResolvedSpec{}))
		Expect(profile.Layers).To(HaveLen(1))
		layers, err := promptLayers(profile, "review.prompt", api.Spec{}, &api.Spec{
			Model: api.Model{Name: "agent:claude-sonnet-5"},
		})
		Expect(err).NotTo(HaveOccurred())
		resolved, err := api.ResolveSpecLayers(api.ResolveSpecOptions{Layers: layers})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Mode).To(Equal(api.ModeAgent))
		Expect(resolved.Spec.Permissions.Tools).To(Equal(api.Tools{"Bash": api.ToolPolicyDeny}))
		Expect(traceNames(resolved)).To(Equal([]string{"Restricted run spec", "review.prompt", "render request"}))
	})

	It("keeps an incomplete chat profile available for a later request", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.profile(runtimeprofiles.ProfileInput{Name: "Restricted", Spec: api.Spec{
			Model:       api.Model{Name: "gpt-5.6-sol", Mode: api.ModeCLI},
			Permissions: api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyDeny}},
		}})
		profile, err := captainChatProfileProvider(GinkgoT().TempDir()).RuntimeProfile(f.ctx, aichat.WithRuntimeProfileRef("restricted"))
		Expect(err).NotTo(HaveOccurred())
		Expect(profile.Composed.Spec.Name).To(Equal("gpt-5.6-sol"))
		Expect(profile.Composed.Spec.Provider).To(BeNil())
		Expect(profile.Composed.Trace).To(HaveExactElements(HaveField("Name", "captain serve"), HaveField("Name", "Restricted run spec")))
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

	It("keeps a selected profile's missing preset as a server error", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.profile(runtimeprofiles.ProfileInput{Name: "Broken", Presets: []string{"missing"}})
		service := aichat.NewService(aichat.ServiceOptions{Profile: captainChatProfileProvider(GinkgoT().TempDir())})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/models?runtimeProfile=broken", nil).WithContext(f.ctx))
		Expect(response.Code).To(Equal(http.StatusInternalServerError), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("missing"))
	})
})
