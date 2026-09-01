package aichat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
)

var _ = Describe("chat session titles", func() {
	const opener = "Update the category and tax_category dimensions on all accounts missing them"

	newService := func(store aichat.ThreadStore, events []api.Event, tools []api.ToolDefinition) (*aichat.Service, *fakeStreamingProvider) {
		provider := &fakeStreamingProvider{events: append(events, api.Event{Kind: api.EventResult, Success: true})}
		options := aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store),
		}
		if len(tools) > 0 {
			options.Tools = aichat.StaticToolProvider(tools)
		}
		return aichat.NewService(options), provider
	}

	submit := func(service *aichat.Service, threadID, text string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			// The api mode: these specs are about message-mode projection, and a bare
			// catalog id would now take openai's agent default.
			ID: threadID, ThreadID: threadID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "openai/test-model", Mode: api.ModeAPI},
			Messages: []aichat.UIMessage{{
				ID: "message-" + threadID, Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: text}},
			}},
		}))
		return response
	}

	It("names an unnamed thread after the message that opened it", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "")
		Expect(err).NotTo(HaveOccurred())
		service, _ := newService(store, nil, nil)

		response := submit(service, thread.ID, opener)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())

		named, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(named.Title).To(Equal(opener))
	})

	It("prefers the title the agent gives itself over the one inferred from the opener", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "")
		Expect(err).NotTo(HaveOccurred())
		service, _ := newService(store, []api.Event{{
			Kind: api.EventToolUse, Tool: session.TitleToolName, ToolCallID: "call-title",
			Input: map[string]any{"aiTitle": "Account dimension backfill"},
		}, {
			Kind: api.EventToolResult, Tool: session.TitleToolName, ToolCallID: "call-title",
			Text: `{"title":"Account dimension backfill"}`, Success: true,
		}}, nil)

		response := submit(service, thread.ID, opener)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())

		named, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(named.Title).To(Equal("Account dimension backfill"))
	})

	It("offers the naming tool and its instruction to a chat that already carries tools", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "")
		Expect(err).NotTo(HaveOccurred())
		service, provider := newService(store, nil, []api.ToolDefinition{{
			Name: "invoice_get", DefaultPermission: api.ToolPolicyAllow,
			Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
		}})

		response := submit(service, thread.ID, opener)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())

		Expect(provider.specs).To(HaveLen(1))
		Expect(provider.specs[0].Messages[0].Role).To(Equal(api.RoleSystem))
		Expect(provider.specs[0].Messages[0].Parts[0].Text).To(ContainSubstring(session.TitleToolName))
		Expect(provider.specs[0].Prompt.AppendSystem).To(BeEmpty(), "message-mode requests must stay message-mode")
	})

	It("leaves a toolless chat without caller tools", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "")
		Expect(err).NotTo(HaveOccurred())
		service, provider := newService(store, nil, nil)

		response := submit(service, thread.ID, opener)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())

		Expect(provider.specs).To(HaveLen(1))
		Expect(provider.specs[0].Prompt.AppendSystem).To(BeEmpty())
	})

	It("renames a thread through the sessions endpoint and keeps that name", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "")
		Expect(err).NotTo(HaveOccurred())
		service, _ := newService(store, []api.Event{{
			Kind: api.EventToolUse, Tool: session.TitleToolName, ToolCallID: "call-title",
			Input: map[string]any{"aiTitle": "Account dimension backfill"},
		}, {
			Kind: api.EventToolResult, Tool: session.TitleToolName, ToolCallID: "call-title",
			Text: `{"title":"Account dimension backfill"}`, Success: true,
		}}, nil)

		rename := httptest.NewRecorder()
		service.Handler().ServeHTTP(rename, requestJSON(http.MethodPatch, "/api/chat/sessions/"+thread.ID,
			map[string]string{"title": "  FY25 dimension cleanup "}))
		Expect(rename.Code).To(Equal(http.StatusOK))
		var renamed aichat.Thread
		Expect(json.Unmarshal(rename.Body.Bytes(), &renamed)).To(Succeed())
		Expect(renamed.Title).To(Equal("FY25 dimension cleanup"))

		response := submit(service, thread.ID, opener)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())

		stored, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Title).To(Equal("FY25 dimension cleanup"))
	})

	It("rejects a blank rename and an unknown thread", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "")
		Expect(err).NotTo(HaveOccurred())
		service, _ := newService(store, nil, nil)

		blank := httptest.NewRecorder()
		service.Handler().ServeHTTP(blank, requestJSON(http.MethodPatch, "/api/chat/sessions/"+thread.ID,
			map[string]string{"title": "   "}))
		Expect(blank.Code).To(Equal(http.StatusBadRequest))

		missing := httptest.NewRecorder()
		service.Handler().ServeHTTP(missing, requestJSON(http.MethodPatch, "/api/chat/sessions/does-not-exist",
			map[string]string{"title": "anything"}))
		Expect(missing.Code).To(Equal(http.StatusNotFound))
	})
})
