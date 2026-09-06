package aichat_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Chat profile composition HTTP", func() {
	serviceFor := func(spec api.Spec, resolver *fakeResolver) *aichat.Service {
		return aichat.NewService(aichat.ServiceOptions{Resolver: resolver,
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
				return aichat.RuntimeProfile{Composed: api.ComposedSpec{Trace: []api.SpecLayer{
					{Name: "application", Scope: api.SpecLayerGlobal, Spec: spec},
				}}}, nil
			}),
		})
	}
	request := aichat.ChatRequest{Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Hello"}}}}}

	It("allows a request runtime to repair structurally valid server defaults", func() {
		provider := &fakeStreamingProvider{}
		resolver := &fakeResolver{provider: provider}
		service := serviceFor(api.Spec{Model: api.Model{Name: "gpt-5.6-sol", Mode: api.ModeCLI},
			Permissions: api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyDeny}},
		}, resolver)
		selected := request
		selected.Model = "agent:claude-sonnet-5"
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", selected))
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(provider.specs).To(HaveLen(1))
		Expect(provider.specs[0].Mode).To(Equal(api.ModeAgent))
		Expect(provider.specs[0].Permissions.Tools).To(Equal(api.Tools{"Bash": api.ToolPolicyDeny}))
	})

	DescribeTable("retains server ownership for malformed defaults", func(spec api.Spec, fragment string) {
		resolver := &fakeResolver{provider: &fakeStreamingProvider{}}
		service := serviceFor(spec, resolver)
		selected := request
		selected.Runtime = &api.Model{Name: "agent:claude-sonnet-5:high"}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", selected))
		Expect(response.Code).To(Equal(http.StatusInternalServerError), response.Body.String())
		Expect(response.Body.String()).To(And(ContainSubstring("application"), ContainSubstring(fragment)))
		Expect(resolver.configs).To(BeEmpty())
	},
		Entry("invalid authored mode", api.Spec{Model: api.Model{Name: "sonnet", Mode: "invalid"}}, "mode"),
		Entry("invalid authored effort", api.Spec{Model: api.Model{Name: "sonnet", Effort: "invalid"}}, "effort"),
	)

	It("serves catalogs for a model-free partial profile without invoking a provider", func() {
		resolver := &fakeResolver{}
		service := serviceFor(api.Spec{Permissions: api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyDeny}}}, resolver)
		for _, path := range []string{"/api/chat/models", "/api/chat/runtimes"} {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		}
		Expect(resolver.configs).To(BeEmpty())
	})

	It("rejects an invalid explicit runtime as a bad request before provider creation", func() {
		resolver := &fakeResolver{}
		service := serviceFor(api.Spec{Model: api.Model{Name: "sonnet", Mode: api.ModeAPI}}, resolver)
		selected := request
		selected.Runtime = &api.Model{Name: "agent:sonnet:high", Mode: "invalid"}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", selected))
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("mode"))
		Expect(resolver.configs).To(BeEmpty())
	})
})
