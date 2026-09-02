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
			{ID: "openai/gpt-5.6-sol", Provider: "openai", Label: "GPT", Runtime: api.Model{Name: "gpt-5.6-sol", Mode: api.ModeAPI}, Configured: true, Availability: api.Available()},
			{ID: "anthropic/claude-sonnet-5", Provider: "anthropic", Label: "Claude", Runtime: api.Model{Name: "claude-sonnet-5", Mode: api.ModeAPI}, Configured: true, Availability: api.Available()},
		}}
		profile := mustRuntimeProfile(api.SpecLayer{
			Name: "claims", Scope: api.SpecLayerContext,
			Constraints: api.RuntimeConstraints{Models: []string{"gpt-5.6-sol"}},
		})
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
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

	It("keeps runtimes enabled when a bare selector admits one of their models", func() {
		resolver := &fakeResolver{runtimes: []api.RuntimeFamily{
			{
				Family: "codex", Provider: "openai", CatalogPrefix: "openai",
				Modes: []api.RuntimeModeEntry{{Mode: "api", Availability: api.Available()}},
			},
			{
				Family: "claude", Provider: "anthropic", CatalogPrefix: "anthropic",
				Modes: []api.RuntimeModeEntry{{Mode: "api", Availability: api.Available()}},
			},
		}}
		profile := mustRuntimeProfile(api.SpecLayer{
			Name: "claims", Scope: api.SpecLayerContext,
			Constraints: api.RuntimeConstraints{Models: []string{"gpt-5.6-sol"}},
		})
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
				return profile, nil
			}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/runtimes", nil))

		Expect(response.Code).To(Equal(http.StatusOK))
		var runtimes []api.RuntimeFamily
		Expect(json.Unmarshal(response.Body.Bytes(), &runtimes)).To(Succeed())
		Expect(runtimes[0].Modes[0].Disabled).To(BeFalse())
		Expect(runtimes[0].Modes[0].Availability).To(Equal(api.Available()))
		Expect(runtimes[1].Modes[0].Disabled).To(BeTrue())
		Expect(runtimes[1].Modes[0].Availability.State).To(Equal(api.AvailabilityDisabled))
	})

	It("reports malformed server profiles as internal errors", func() {
		cases := []struct {
			name     string
			resolved api.ResolvedSpec
			message  string
		}{
			{
				name: "missing trace",
				resolved: api.ResolvedSpec{Spec: api.Spec{
					Model: api.Model{Name: "gpt-5.4"},
				}},
				message: "must include its resolution trace",
			},
			{
				name: "invalid trace",
				resolved: api.ResolvedSpec{Trace: []api.SpecLayer{{
					Name: "broken", Scope: api.SpecLayerScope("invalid"),
				}}},
				message: "invalid scope",
			},
		}

		for _, test := range cases {
			service := aichat.NewService(aichat.ServiceOptions{
				Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
					return aichat.RuntimeProfile{Resolved: test.resolved}, nil
				}),
			})
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "hello"}}}},
			}))

			Expect(response.Code).To(Equal(http.StatusInternalServerError), test.name)
			Expect(response.Body.String()).To(ContainSubstring(test.message), test.name)
		}
	})

	It("keeps request model violations as bad requests", func() {
		profile := mustRuntimeProfile(api.SpecLayer{
			Name: "claims", Scope: api.SpecLayerContext,
			Spec:        api.Spec{Model: api.Model{Name: "gpt-5.6-sol"}},
			Constraints: api.RuntimeConstraints{Models: []string{"gpt-5.6-sol"}},
		})
		service := aichat.NewService(aichat.ServiceOptions{
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
				return profile, nil
			}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Model:    "claude-sonnet-5",
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "hello"}}}},
		}))

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring("outside the effective model catalog"))
	})

	It("checks every named quota and reports the independently exhausted allowance", func() {
		resolver := &fakeResolver{provider: &fakeStreamingProvider{}}
		profile := mustRuntimeProfile(
			api.SpecLayer{
				Name: "platform", Scope: api.SpecLayerGlobal,
				Spec:        api.Spec{Model: api.Model{Name: "openai/test-model"}},
				Constraints: api.RuntimeConstraints{Quotas: []api.UsageQuota{{Name: "platform-monthly", TokenLimit: 100, TokensUsed: 10}}},
			},
			api.SpecLayer{
				Name: "claims", Scope: api.SpecLayerContext,
				Constraints: api.RuntimeConstraints{Quotas: []api.UsageQuota{{Name: "claims-monthly", CostLimitUSD: 20, CostUsedUSD: 20}}},
			},
		)
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
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
