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
	It("reports malformed server profiles as internal errors", func() {
		cases := []struct {
			name     string
			composed api.ComposedSpec
			message  string
		}{
			{
				name: "missing trace",
				composed: api.ComposedSpec{Spec: api.Spec{
					Model: api.Model{Name: "gpt-5.4"},
				}},
				message: "must include its composition trace",
			},
			{
				name: "invalid trace",
				composed: api.ComposedSpec{Trace: []api.SpecLayer{{
					Name: "broken", Scope: api.SpecLayerScope("invalid"),
				}}},
				message: "invalid scope",
			},
		}

		for _, test := range cases {
			service := aichat.NewService(aichat.ServiceOptions{
				Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
					return aichat.RuntimeProfile{Composed: test.composed}, nil
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

	// The profile supplies defaults, so every model the deployment serves stays
	// selectable: a profile naming one model does not withdraw the others.
	It("leaves the served catalog available when a profile names one model", func() {
		resolver := &fakeResolver{models: aichat.ModelCatalogResponse{
			{ID: "openai/gpt-5.6-sol", Provider: "openai", Label: "GPT", Runtime: api.Model{Name: "gpt-5.6-sol", Mode: api.ModeAPI}, Configured: true, Availability: api.Available()},
			{ID: "anthropic/claude-sonnet-5", Provider: "anthropic", Label: "Claude", Runtime: api.Model{Name: "claude-sonnet-5", Mode: api.ModeAPI}, Configured: true, Availability: api.Available()},
		}}
		profile := mustRuntimeProfile(api.SpecLayer{
			Name: "claims", Scope: api.SpecLayerContext,
			Spec: api.Spec{Model: api.Model{Name: "gpt-5.6-sol"}},
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
		Expect(models[1].Configured).To(BeTrue())
		Expect(models[1].Availability).To(Equal(api.Available()))
	})
})

func mustRuntimeProfile(layers ...api.SpecLayer) aichat.RuntimeProfile {
	composed, err := api.ComposeSpecLayers(api.ResolveSpecOptions{Layers: layers})
	Expect(err).NotTo(HaveOccurred())
	return aichat.RuntimeProfile{Composed: composed}
}
