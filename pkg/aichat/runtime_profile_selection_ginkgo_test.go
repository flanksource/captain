package aichat_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
)

type recordingProfileProvider struct {
	profile    aichat.RuntimeProfile
	err        error
	selections []aichat.RuntimeProfileOptions
}

func (p *recordingProfileProvider) RuntimeProfile(_ context.Context, options ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
	p.selections = append(p.selections, aichat.ApplyRuntimeProfileOptions(options...))
	return p.profile, p.err
}

var _ = Describe("Runtime profile selection", func() {
	userMessages := []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "hello"}}}}
	applicationProfile := func() aichat.RuntimeProfile {
		return mustRuntimeProfile(api.SpecLayer{
			Name: "application", Scope: api.SpecLayerGlobal,
			Spec: api.Spec{Model: api.Model{Name: "openai/test-model", Mode: api.ModeAPI}},
		})
	}

	It("hands the chat request's runtimeProfile to the provider", func() {
		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "done"},
			{Kind: api.EventResult, Success: true, Model: "test-model"},
		}}
		profiles := &recordingProfileProvider{profile: applicationProfile()}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}, Profile: profiles})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			RuntimeProfile: "review", Messages: userMessages,
		}))

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(profiles.selections).To(Equal([]aichat.RuntimeProfileOptions{{Ref: "review"}}))
	})

	It("hands ?runtimeProfile= on the catalog endpoints to the provider", func() {
		for _, path := range []string{"/api/chat/models", "/api/chat/runtimes"} {
			profiles := &recordingProfileProvider{profile: applicationProfile()}
			service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{}, Profile: profiles})

			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"?runtimeProfile=review", nil))

			Expect(response.Code).To(Equal(http.StatusOK), path)
			Expect(profiles.selections).To(Equal([]aichat.RuntimeProfileOptions{{Ref: "review"}}), path)
		}
	})

	It("reports a rejected selection with the provider's status and message", func() {
		const message = `runtime profile "bogus" is not in the catalog`
		profiles := &recordingProfileProvider{err: aichat.RequestError(http.StatusBadRequest, message)}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{}, Profile: profiles})

		for _, request := range []*http.Request{
			requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{RuntimeProfile: "bogus", Messages: userMessages}),
			httptest.NewRequest(http.MethodGet, "/api/chat/models?runtimeProfile=bogus", nil),
			httptest.NewRequest(http.MethodGet, "/api/chat/runtimes?runtimeProfile=bogus", nil),
		} {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, request)
			Expect(response.Code).To(Equal(http.StatusBadRequest), request.URL.Path)
			Expect(response.Body.String()).To(ContainSubstring(message), request.URL.Path)
		}
	})

	It("rejects a selection when the deployment serves no runtime profiles", func() {
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{}})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			RuntimeProfile: "review", Messages: userMessages,
		}))

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring(`runtime profile "review" cannot be selected`))
	})

	It("keeps other provider failures as internal errors", func() {
		profiles := &recordingProfileProvider{err: errors.New("profile catalog unavailable")}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{}, Profile: profiles})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{Messages: userMessages}))

		Expect(response.Code).To(Equal(http.StatusInternalServerError))
		Expect(response.Body.String()).To(ContainSubstring("profile catalog unavailable"))
	})

	It("loads the deployment default profile for approval continuations", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Contacts")
		Expect(err).NotTo(HaveOccurred())
		authority := &fakeExecutionAuthority{continuation: &aichat.ApprovalContinuation{
			Execution: &fakeExecution{},
			Spec:      api.Spec{Model: api.Model{Name: "gpt-5.6-sol"}, ToolApproval: &api.ToolApprovalResume{}},
		}}
		profiles := &recordingProfileProvider{profile: mustRuntimeProfile(api.SpecLayer{
			Name: "claims", Scope: api.SpecLayerContext,
			Constraints: api.RuntimeConstraints{Models: []string{"claude-sonnet-5"}},
		})}
		service := aichat.NewService(aichat.ServiceOptions{
			Threads: aichat.FixedThreadStore(store), Authority: authority,
			Resolver: &fakeResolver{provider: &fakeStreamingProvider{}}, Profile: profiles,
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(
			http.MethodPost,
			"/api/chat/sessions/"+thread.ID+"/approvals/0e5dc2fe-8b77-44e9-a3de-6a00298c8bde",
			map[string]any{"approved": true},
		))

		Expect(response.Code).To(Equal(http.StatusBadGateway), response.Body.String())
		Expect(profiles.selections).To(Equal([]aichat.RuntimeProfileOptions{{}}))
	})
})
