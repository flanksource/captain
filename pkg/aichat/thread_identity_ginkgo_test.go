package aichat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type staleRuntimeThreadStore struct {
	aichat.ThreadStore
}

type blockingForkThreadStore struct {
	aichat.ThreadStore
	started chan<- struct{}
	release <-chan struct{}
}

type blockingGetThreadStore struct {
	aichat.ThreadStore
	enabled         atomic.Bool
	blocked         atomic.Int32
	approvalStarted chan<- struct{}
	approvalRelease <-chan struct{}
	chatStarted     chan<- struct{}
	chatRelease     <-chan struct{}
}

func (s *blockingGetThreadStore) Get(ctx context.Context, id string) (*aichat.Thread, error) {
	if s.enabled.Load() {
		switch s.blocked.Add(1) {
		case 1:
			s.approvalStarted <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-s.approvalRelease:
			}
		case 2:
			s.chatStarted <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-s.chatRelease:
			}
		}
	}
	return s.ThreadStore.Get(ctx, id)
}

func (s blockingForkThreadStore) Fork(ctx context.Context, id string) (*aichat.Thread, error) {
	s.started <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return s.ThreadStore.Fork(ctx, id)
	}
}

func (s staleRuntimeThreadStore) Get(ctx context.Context, id string) (*aichat.Thread, error) {
	thread, err := s.ThreadStore.Get(ctx, id)
	if thread != nil {
		thread.Runtime = nil
	}
	return thread, err
}

var _ = Describe("Authoritative chat thread identity", func() {
	It("uses stored history and locks only model identity after the first admitted turn", func() {
		ctx := context.Background()
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(ctx, "Identity")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
			ID: "persisted-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Persisted question"}},
		})).To(Succeed())
		Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
			ID: "persisted-assistant", Role: "assistant", Parts: []aichat.UIPart{{Type: "text", Text: "Persisted answer"}},
		})).To(Succeed())
		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "New answer"},
			{Kind: api.EventResult, Success: true, SessionID: "provider-session-1"},
		}}
		resolver := &fakeResolver{provider: provider}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver, Threads: aichat.FixedThreadStore(store),
		})

		submit := func(id string, runtime api.Model, clientHistory ...aichat.UIMessage) *httptest.ResponseRecorder {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message", Runtime: &runtime,
				Messages: append(clientHistory, aichat.UIMessage{
					ID: id, Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: id}},
				}),
			}))
			return response
		}

		first := submit("new-user-1", api.Model{Name: "test-model", Backend: api.BackendOpenAI},
			aichat.UIMessage{ID: "stale", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Stale client history"}}})
		Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())
		Expect(provider.specs).To(HaveLen(1))
		encoded, err := json.Marshal(provider.specs[0].Messages)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring("Persisted question"))
		Expect(string(encoded)).To(ContainSubstring("Persisted answer"))
		Expect(string(encoded)).NotTo(ContainSubstring("Stale client history"))

		bound, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bound.Runtime).To(Equal(&api.Model{Name: "test-model", Backend: api.BackendOpenAI}))
		Expect(bound.ProviderSessionID).To(Equal("provider-session-1"))

		sameModel := submit("new-user-2", api.Model{
			Name: "test-model", Backend: api.BackendOpenAI, Effort: api.EffortHigh,
		})
		Expect(sameModel.Code).To(Equal(http.StatusOK), sameModel.Body.String())
		Expect(provider.specs).To(HaveLen(2))
		Expect(provider.specs[1].Model.Effort).To(Equal(api.EffortHigh))
		Expect(resolver.configs[1].SessionID).To(Equal("provider-session-1"))

		beforeConflict, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		mismatch := submit("new-user-3", api.Model{Name: "claude-sonnet-4-6", Backend: api.BackendAnthropic})
		Expect(mismatch.Code).To(Equal(http.StatusConflict))
		Expect(mismatch.Body.String()).To(ContainSubstring("openai/test-model"))
		Expect(mismatch.Body.String()).To(ContainSubstring("anthropic/claude-sonnet-4-6"))
		Expect(strings.ToLower(mismatch.Body.String())).To(ContainSubstring("fork"))
		Expect(provider.specs).To(HaveLen(2), "a rejected resume must not launch the provider")
		afterConflict, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(afterConflict.Messages).To(Equal(beforeConflict.Messages), "a rejected resume must not persist")
	})

	It("does not persist a message when a concurrent runtime binding wins admission", func() {
		ctx := context.Background()
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(ctx, "Runtime race")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.SetRuntime(ctx, thread.ID, api.Model{
			Name: "bound-model", Backend: api.BackendOpenAI,
		})).To(Succeed())
		provider := &fakeStreamingProvider{}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider},
			Threads:  aichat.FixedThreadStore(staleRuntimeThreadStore{ThreadStore: store}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "other-model", Backend: api.BackendOpenAI},
			Messages: []aichat.UIMessage{{
				ID: "losing-message", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Do not persist"}},
			}},
		}))

		Expect(response.Code).To(Equal(http.StatusConflict), response.Body.String())
		Expect(provider.specs).To(BeEmpty(), "a rejected runtime must not launch the provider")
		stored, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Messages).To(BeEmpty())
	})

	It("rejects a second admission and a fork while the first turn is active", func() {
		ctx := context.Background()
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(ctx, "Concurrent")
		Expect(err).NotTo(HaveOccurred())
		started := make(chan struct{})
		release := make(chan struct{})
		provider := &fakeStreamingProvider{execute: func(context.Context, api.Spec) (<-chan api.Event, error) {
			events := make(chan api.Event)
			close(started)
			go func() {
				<-release
				close(events)
			}()
			return events, nil
		}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store),
		})
		submit := func(messageID string) *httptest.ResponseRecorder {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
				Runtime: &api.Model{Name: "test-model", Backend: api.BackendOpenAI},
				Messages: []aichat.UIMessage{{
					ID: messageID, Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: messageID}},
				}},
			}))
			return response
		}

		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() { firstDone <- submit("first-user") }()
		Eventually(started).Should(BeClosed())

		second := submit("second-user")
		Expect(second.Code).To(Equal(http.StatusConflict))
		Expect(second.Body.String()).To(ContainSubstring("active turn"))
		fork := httptest.NewRecorder()
		service.Handler().ServeHTTP(fork, httptest.NewRequest(http.MethodPost,
			"/api/chat/sessions/"+thread.ID+"/fork", nil))
		Expect(fork.Code).To(Equal(http.StatusConflict))

		close(release)
		Eventually(firstDone).Should(Receive())
		Expect(provider.specs).To(HaveLen(1))
		stored, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Messages).NotTo(ContainElement(HaveField("ID", "second-user")))
	})

	It("reserves a suspended approval before relaunching its provider", func() {
		ctx := context.Background()
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(ctx, "Approval resume")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.SetRuntime(ctx, thread.ID, api.Model{Name: "test-model", Backend: api.BackendOpenAI})).To(Succeed())
		Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
			ID: "approval-user", Role: "user", TurnID: "turn-1",
			Parts: []aichat.UIPart{{Type: "text", Text: "Update the account"}},
		})).To(Succeed())
		Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
			ID: "turn-1-assistant", Role: "assistant", TurnID: "turn-1",
			Parts: []aichat.UIPart{{
				Type: "dynamic-tool", ToolName: "accounts_edit", ToolCallID: "call-1", State: "approval-requested",
			}},
		})).To(Succeed())

		providerStarted := make(chan struct{})
		releaseProvider := make(chan struct{})
		approvalGetStarted := make(chan struct{})
		releaseApprovalGet := make(chan struct{})
		chatGetStarted := make(chan struct{})
		releaseChatGet := make(chan struct{})
		blockingStore := &blockingGetThreadStore{
			ThreadStore: store, approvalStarted: approvalGetStarted, approvalRelease: releaseApprovalGet,
			chatStarted: chatGetStarted, chatRelease: releaseChatGet,
		}
		var launches atomic.Int32
		provider := &fakeStreamingProvider{execute: func(context.Context, api.Spec) (<-chan api.Event, error) {
			launch := launches.Add(1)
			if launch == 1 {
				close(providerStarted)
				<-releaseProvider
				blockingStore.enabled.Store(true)
			}
			events := make(chan api.Event, 2)
			if launch == 1 {
				events <- api.Event{Kind: api.EventToolResult, ToolCallID: "call-1", Tool: "accounts_edit", Success: true}
			} else {
				events <- api.Event{Kind: api.EventText, Text: "Next answer"}
			}
			events <- api.Event{Kind: api.EventResult, Success: true}
			close(events)
			return events, nil
		}}
		execution := &fakeExecution{}
		authority := &fakeExecutionAuthority{continuation: &aichat.ApprovalContinuation{
			Execution: execution,
			Spec: api.Spec{
				Model:        api.Model{Name: "test-model", Backend: api.BackendOpenAI},
				ToolApproval: &api.ToolApprovalResume{},
			},
		}, execution: &fakeExecution{}}
		service := aichat.NewService(aichat.ServiceOptions{
			Threads: aichat.FixedThreadStore(blockingStore), Authority: authority,
			Resolver: &fakeResolver{provider: provider},
		})

		approvalDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(
				http.MethodPost,
				"/api/chat/sessions/"+thread.ID+"/approvals/approval-1",
				map[string]any{"approved": true},
			))
			approvalDone <- response
		}()
		Eventually(providerStarted).Should(BeClosed())

		concurrent := httptest.NewRecorder()
		service.Handler().ServeHTTP(concurrent, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "test-model", Backend: api.BackendOpenAI},
			Messages: []aichat.UIMessage{{
				ID: "concurrent-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Race the approval"}},
			}},
		}))
		Expect(concurrent.Code).To(Equal(http.StatusConflict), concurrent.Body.String())
		fork := httptest.NewRecorder()
		service.Handler().ServeHTTP(fork, httptest.NewRequest(http.MethodPost,
			"/api/chat/sessions/"+thread.ID+"/fork", nil))
		Expect(fork.Code).To(Equal(http.StatusConflict), fork.Body.String())
		Expect(launches.Load()).To(Equal(int32(1)), "no second provider may launch while approval continuation is active")

		close(releaseProvider)
		Eventually(approvalGetStarted).Should(Receive())
		nextDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
				Runtime: &api.Model{Name: "test-model", Backend: api.BackendOpenAI},
				Messages: []aichat.UIMessage{{
					ID: "next-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Continue after approval"}},
				}},
			}))
			nextDone <- response
		}()
		Eventually(chatGetStarted).Should(Receive())
		close(releaseApprovalGet)
		var approval *httptest.ResponseRecorder
		Eventually(approvalDone).Should(Receive(&approval))
		Expect(approval.Code).To(Equal(http.StatusOK), approval.Body.String())
		close(releaseChatGet)
		var next *httptest.ResponseRecorder
		Eventually(nextDone).Should(Receive(&next))
		Expect(next.Code).To(Equal(http.StatusOK), next.Body.String())
		stored, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Messages).NotTo(ContainElement(HaveField("ID", "concurrent-user")))
		Expect(stored.Messages).To(ContainElement(HaveField("ID", "next-user")))
	})

	It("reserves a fork source against concurrent turn admission", func() {
		ctx := context.Background()
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(ctx, "Fork race")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
			ID: "source-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Source"}},
		})).To(Succeed())
		forkStarted := make(chan struct{}, 1)
		releaseFork := make(chan struct{})
		provider := &fakeStreamingProvider{}
		service := aichat.NewService(aichat.ServiceOptions{
			Threads: aichat.FixedThreadStore(blockingForkThreadStore{
				ThreadStore: store, started: forkStarted, release: releaseFork,
			}),
			Resolver: &fakeResolver{provider: provider},
		})

		forkDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost,
				"/api/chat/sessions/"+thread.ID+"/fork", nil))
			forkDone <- response
		}()
		Eventually(forkStarted).Should(Receive())

		concurrent := httptest.NewRecorder()
		service.Handler().ServeHTTP(concurrent, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "test-model", Backend: api.BackendOpenAI},
			Messages: []aichat.UIMessage{{
				ID: "concurrent-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Race the fork"}},
			}},
		}))
		Expect(concurrent.Code).To(Equal(http.StatusConflict), concurrent.Body.String())
		Expect(provider.specs).To(BeEmpty())

		close(releaseFork)
		var response *httptest.ResponseRecorder
		Eventually(forkDone).Should(Receive(&response))
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
	})

	It("creates an independent root fork with one turnless transcript seed", func() {
		ctx := context.Background()
		store := aichat.NewMemoryThreadStore()
		source, err := store.Create(ctx, "Source chat")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.AppendMessage(ctx, source.ID, aichat.UIMessage{
			ID: "source-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Question"}},
		})).To(Succeed())
		Expect(store.AppendMessage(ctx, source.ID, aichat.UIMessage{
			ID: "source-assistant", Role: "assistant", Parts: []aichat.UIPart{{Type: "text", Text: "Answer"}},
		})).To(Succeed())
		Expect(store.SetProviderSession(ctx, source.ID, "provider-source")).To(Succeed())
		Expect(store.SetRuntime(ctx, source.ID, api.Model{Name: "test-model", Backend: api.BackendOpenAI})).To(Succeed())
		_, err = store.AddUsage(ctx, source.ID, aichat.TurnUsage{InputTokens: 10, OutputTokens: 4, CostUSD: 0.2})
		Expect(err).NotTo(HaveOccurred())
		service := aichat.NewService(aichat.ServiceOptions{Threads: aichat.FixedThreadStore(store)})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost,
			"/api/chat/sessions/"+source.ID+"/fork", nil))
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
		var fork aichat.Thread
		Expect(json.Unmarshal(response.Body.Bytes(), &fork)).To(Succeed())
		Expect(fork.ID).NotTo(Equal(source.ID))
		Expect(fork.Title).To(Equal("Fork of Source chat"))
		Expect(fork.ForkedFrom).To(Equal(source.ID))
		Expect(fork.ProviderSessionID).To(BeEmpty())
		Expect(fork.Runtime).To(BeNil())
		Expect(fork.TotalInputTokens).To(BeZero())
		Expect(fork.TotalCostUSD).To(BeZero())
		Expect(fork.Messages).To(HaveLen(1))
		Expect(fork.Messages[0].TurnID).To(BeEmpty())
		Expect(fork.Messages[0].Parts).To(ContainElement(HaveField("Type", "data-fork-seed")))
		Expect(fork.Messages[0].Parts[1].Text).To(ContainSubstring("<captain-fork"))
		Expect(fork.Messages[0].Parts[1].Text).To(ContainSubstring("Question"))
		Expect(fork.Messages[0].Parts[1].Text).To(ContainSubstring("Answer"))
		Expect(fork.Messages[0].Parts[1].Text).To(ContainSubstring("</captain-fork>"))

		list := httptest.NewRecorder()
		service.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/chat/sessions", nil))
		var summaries []aichat.Thread
		Expect(json.Unmarshal(list.Body.Bytes(), &summaries)).To(Succeed())
		Expect(summaries).To(HaveLen(2))
		for _, summary := range summaries {
			Expect(summary.Messages).To(BeNil())
		}

		seedOnly := httptest.NewRecorder()
		service.Handler().ServeHTTP(seedOnly, httptest.NewRequest(http.MethodPost,
			"/api/chat/sessions/"+fork.ID+"/fork", nil))
		Expect(seedOnly.Code).To(Equal(http.StatusBadRequest))

		Expect(store.AppendMessage(ctx, fork.ID, aichat.UIMessage{
			ID: "fork-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Follow-up question"}},
		})).To(Succeed())
		Expect(store.AppendMessage(ctx, fork.ID, aichat.UIMessage{
			ID: "fork-assistant", Role: "assistant", Parts: []aichat.UIPart{{Type: "text", Text: "Follow-up answer"}},
		})).To(Succeed())
		nestedResponse := httptest.NewRecorder()
		service.Handler().ServeHTTP(nestedResponse, httptest.NewRequest(http.MethodPost,
			"/api/chat/sessions/"+fork.ID+"/fork", nil))
		Expect(nestedResponse.Code).To(Equal(http.StatusCreated), nestedResponse.Body.String())
		var nested aichat.Thread
		Expect(json.Unmarshal(nestedResponse.Body.Bytes(), &nested)).To(Succeed())
		Expect(nested.Messages).To(HaveLen(1))
		Expect(nested.Messages[0].TurnID).To(BeEmpty())
		Expect(nested.Messages[0].Parts).To(ContainElement(HaveField("Type", "data-fork-seed")))
		Expect(nested.Messages[0].Parts[1].Text).To(ContainSubstring("Question"))
		Expect(nested.Messages[0].Parts[1].Text).To(ContainSubstring("Answer"))
		Expect(nested.Messages[0].Parts[1].Text).To(ContainSubstring("Follow-up question"))
		Expect(nested.Messages[0].Parts[1].Text).To(ContainSubstring("Follow-up answer"))

		missing := httptest.NewRecorder()
		service.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodPost,
			"/api/chat/sessions/missing/fork", nil))
		Expect(missing.Code).To(Equal(http.StatusNotFound))
	})

	It("sends the fork seed once to agent runtimes and skips its marker for API runtimes", func() {
		ctx := context.Background()
		store := aichat.NewMemoryThreadStore()
		source, err := store.Create(ctx, "Seed source")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.AppendMessage(ctx, source.ID, aichat.UIMessage{
			ID: "source-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Original question"}},
		})).To(Succeed())

		agentFork, err := store.Fork(ctx, source.ID)
		Expect(err).NotTo(HaveOccurred())
		agentProvider := &fakeStreamingProvider{
			backend: api.BackendClaudeAgent,
			events:  []api.Event{{Kind: api.EventResult, Success: true, SessionID: "provider-fork"}},
		}
		agentResolver := &fakeResolver{provider: agentProvider}
		agentService := aichat.NewService(aichat.ServiceOptions{
			Resolver: agentResolver, Threads: aichat.FixedThreadStore(store),
		})
		submitAgent := func(id, text string) *httptest.ResponseRecorder {
			response := httptest.NewRecorder()
			agentService.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				ID: agentFork.ID, ThreadID: agentFork.ID, Trigger: "submit-message",
				Runtime: &api.Model{Name: "sonnet", Backend: api.BackendClaudeAgent},
				Messages: []aichat.UIMessage{{
					ID: id, Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: text}},
				}},
			}))
			return response
		}

		first := submitAgent("agent-user-1", "Continue on agent")
		Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())
		Expect(agentProvider.specs).To(HaveLen(1))
		Expect(agentProvider.specs[0].Messages).To(BeNil())
		Expect(agentProvider.specs[0].Prompt.User).To(ContainSubstring("<captain-fork"))
		Expect(agentProvider.specs[0].Prompt.User).To(ContainSubstring("Original question"))
		Expect(agentProvider.specs[0].Prompt.User).To(ContainSubstring("Continue on agent"))

		second := submitAgent("agent-user-2", "Second agent turn")
		Expect(second.Code).To(Equal(http.StatusOK), second.Body.String())
		Expect(agentProvider.specs).To(HaveLen(2))
		Expect(agentProvider.specs[1].Prompt.User).To(Equal("Second agent turn"))
		Expect(agentProvider.specs[1].Prompt.User).NotTo(ContainSubstring("<captain-fork"))
		Expect(agentResolver.configs[1].SessionID).To(Equal("provider-fork"))

		apiFork, err := store.Fork(ctx, source.ID)
		Expect(err).NotTo(HaveOccurred())
		apiProvider := &fakeStreamingProvider{events: []api.Event{{Kind: api.EventResult, Success: true}}}
		apiService := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: apiProvider}, Threads: aichat.FixedThreadStore(store),
		})
		apiResponse := httptest.NewRecorder()
		apiService.Handler().ServeHTTP(apiResponse, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: apiFork.ID, ThreadID: apiFork.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "test-model", Backend: api.BackendOpenAI},
			Messages: []aichat.UIMessage{{
				ID: "api-user-1", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Continue on API"}},
			}},
		}))

		Expect(apiResponse.Code).To(Equal(http.StatusOK), apiResponse.Body.String())
		Expect(apiProvider.specs).To(HaveLen(1))
		Expect(apiProvider.specs[0].Messages).To(HaveLen(2))
		Expect(apiProvider.specs[0].Messages[0].Parts).To(HaveLen(1), "the data-fork-seed marker is UI-only")
		Expect(apiProvider.specs[0].Messages[0].Parts[0].Text).To(ContainSubstring("<captain-fork"))
		Expect(apiProvider.specs[0].Messages[1].Parts[0].Text).To(Equal("Continue on API"))
	})

	It("bounds thread summaries and hydrates full detail separately", func() {
		ctx := context.Background()
		store := aichat.NewMemoryThreadStore()
		var detailID string
		for i := 0; i < 101; i++ {
			thread, err := store.Create(ctx, "Summary")
			Expect(err).NotTo(HaveOccurred())
			Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
				ID: "message-" + thread.ID, Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "content"}},
			})).To(Succeed())
			detailID = thread.ID
		}
		Expect(store.SetRuntime(ctx, detailID, api.Model{Name: "test-model", Backend: api.BackendOpenAI})).To(Succeed())
		service := aichat.NewService(aichat.ServiceOptions{Threads: aichat.FixedThreadStore(store)})

		list := httptest.NewRecorder()
		service.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/chat/sessions", nil))
		var summaries []aichat.Thread
		Expect(json.Unmarshal(list.Body.Bytes(), &summaries)).To(Succeed())
		Expect(summaries).To(HaveLen(100))
		for _, summary := range summaries {
			Expect(summary.Messages).To(BeNil())
		}

		detail := httptest.NewRecorder()
		service.Handler().ServeHTTP(detail, httptest.NewRequest(http.MethodGet,
			"/api/chat/sessions/"+detailID, nil))
		var hydrated aichat.Thread
		Expect(json.Unmarshal(detail.Body.Bytes(), &hydrated)).To(Succeed())
		Expect(hydrated.Messages).To(HaveLen(1))
		Expect(hydrated.Runtime).To(Equal(&api.Model{Name: "test-model", Backend: api.BackendOpenAI}))
	})
})
