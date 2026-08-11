package aichat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resolved runtime profiles", func() {
	It("annotates models outside the effective catalog with actionable provenance", func() {
		resolver := &fakeResolver{models: aichat.ModelCatalogResponse{
			{ID: "openai/gpt-5.6-sol", Provider: "openai", Label: "GPT", Runtime: api.Model{Name: "gpt-5.6-sol", Backend: api.BackendOpenAI}, Configured: true, Availability: api.Available()},
			{ID: "anthropic/claude-sonnet-5", Provider: "anthropic", Label: "Claude", Runtime: api.Model{Name: "claude-sonnet-5", Backend: api.BackendAnthropic}, Configured: true, Availability: api.Available()},
		}}
		profile := mustRuntimeProfile(api.SpecLayer{
			Name: "claims", Scope: api.SpecLayerContext,
			Constraints: api.RuntimeConstraints{Models: []string{"gpt-5.6-sol"}},
		})
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context) (aichat.RuntimeProfile, error) {
				return profile, nil
			}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/models", nil))

		Expect(response.Code).To(Equal(http.StatusOK))
		var models aichat.ModelCatalogResponse
		Expect(json.Unmarshal(response.Body.Bytes(), &models)).To(Succeed())
		Expect(models[0].Availability).To(Equal(api.Available()))
		Expect(models[1].Configured).To(BeFalse())
		Expect(models[1].Availability.State).To(Equal(api.AvailabilityDisabled))
		Expect(models[1].Availability.Reason).To(ContainSubstring(`context layer "claims"`))
		Expect(models[1].Availability.Remediation).To(ContainSubstring("allowed models"))
	})

	It("checks every named quota and reports the independently exhausted allowance", func() {
		resolver := &fakeResolver{provider: &fakeStreamingProvider{}}
		profile := mustRuntimeProfile(
			api.SpecLayer{
				Name: "platform", Scope: api.SpecLayerGlobal,
				Spec:        api.Spec{Model: api.Model{Name: "openai/test-model"}},
				Constraints: api.RuntimeConstraints{Quotas: []api.RuntimeQuota{{Name: "platform-monthly", TokenLimit: 100, TokensUsed: 10}}},
			},
			api.SpecLayer{
				Name: "claims", Scope: api.SpecLayerContext,
				Constraints: api.RuntimeConstraints{Quotas: []api.RuntimeQuota{{Name: "claims-monthly", CostLimitUSD: 20, CostUsedUSD: 20}}},
			},
		)
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context) (aichat.RuntimeProfile, error) {
				return profile, nil
			}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "hello"}}}},
		}))

		Expect(response.Code).To(Equal(http.StatusPaymentRequired))
		Expect(response.Body.String()).To(ContainSubstring(`context quota "claims-monthly" from layer "claims"`))
		Expect(resolver.configs).To(BeEmpty())
	})
})

func mustRuntimeProfile(layers ...api.SpecLayer) aichat.RuntimeProfile {
	resolved, err := api.ResolveSpecLayers(layers...)
	Expect(err).NotTo(HaveOccurred())
	return aichat.RuntimeProfile{Resolved: resolved}
}
