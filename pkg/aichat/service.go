package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
)

var serviceLog = logger.GetLogger("aichat")

// RuntimeProfile is the request-scoped, hierarchically resolved application
// configuration for a chat. Resolved carries the effective Spec, constraints,
// and ordered provenance; provider credentials remain runtime-only.
type RuntimeProfile struct {
	System         string
	Resolved       api.ResolvedSpec
	ProviderConfig api.Config
}

// RuntimeProfileProvider supplies request-scoped application profiles.
type RuntimeProfileProvider interface {
	RuntimeProfile(context.Context) (RuntimeProfile, error)
}

type RuntimeProfileProviderFunc func(context.Context) (RuntimeProfile, error)

func (f RuntimeProfileProviderFunc) RuntimeProfile(ctx context.Context) (RuntimeProfile, error) {
	return f(ctx)
}

// ThreadStoreProvider supplies the request-scoped thread store. Applications
// that can serve more than one database resolve it per request; a fixed store
// is expressed as a provider that ignores the context.
type ThreadStoreProvider interface {
	ThreadStore(context.Context) (ThreadStore, error)
}

type ThreadStoreProviderFunc func(context.Context) (ThreadStore, error)

func (f ThreadStoreProviderFunc) ThreadStore(ctx context.Context) (ThreadStore, error) {
	return f(ctx)
}

// FixedThreadStore adapts a single store to the provider interface.
func FixedThreadStore(store ThreadStore) ThreadStoreProvider {
	return ThreadStoreProviderFunc(func(context.Context) (ThreadStore, error) { return store, nil })
}

// ServiceOptions injects every application-owned chat dependency. A nil
// Resolver uses Captain's canonical model/provider resolver.
type ServiceOptions struct {
	Resolver       Resolver
	ProviderConfig ProviderConfigSource
	Profile        RuntimeProfileProvider
	Tools          ToolProvider
	MCP            ToolProvider
	Attachments    AttachmentResolver
	Threads        ThreadStoreProvider
	Authority      ExecutionAuthority
}

// Service is Captain's AI SDK-compatible HTTP chat service.
type Service struct {
	options  ServiceOptions
	resolver Resolver
	activeMu sync.Mutex
	active   map[string]*activeTurn
}

func NewService(options ServiceOptions) *Service {
	resolver := options.Resolver
	if resolver == nil {
		resolver = captainResolver{}
	}
	return &Service{options: options, resolver: resolver, active: map[string]*activeTurn{}}
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("GET /api/chat/models", s.handleModels)
	mux.HandleFunc("GET /api/chat/runtimes", s.handleRuntimes)
	mux.HandleFunc("GET /api/chat/tools", s.handleTools)
	s.registerThreadRoutes(mux)
	return mux
}

func (s *Service) handleRuntimes(w http.ResponseWriter, request *http.Request) {
	profile, err := s.runtimeProfile(request.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("load chat runtime profile: %v", err), http.StatusInternalServerError)
		return
	}
	runtimes, err := s.resolver.Runtimes(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := s.annotateConfiguredRuntimes(request.Context(), runtimes); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	annotateProfileRuntimes(profile.Resolved, runtimes)
	if err := writeJSON(w, http.StatusOK, runtimes); err != nil {
		serviceLog.Errorf("write chat runtimes response: %v", err)
	}
}

func (s *Service) handleModels(w http.ResponseWriter, request *http.Request) {
	profile, err := s.runtimeProfile(request.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("load chat runtime profile: %v", err), http.StatusInternalServerError)
		return
	}
	models, err := s.resolver.Models(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := s.annotateConfiguredModels(request.Context(), models); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	annotateProfileModels(profile.Resolved, models)
	if err := writeJSON(w, http.StatusOK, models); err != nil {
		serviceLog.Errorf("write chat models response: %v", err)
	}
}

func (s *Service) handleTools(w http.ResponseWriter, request *http.Request) {
	if _, err := s.runtimeProfile(request.Context()); err != nil {
		http.Error(w, fmt.Sprintf("load chat runtime profile: %v", err), http.StatusInternalServerError)
		return
	}
	set, err := s.loadTools(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeJSON(w, http.StatusOK, aitools.ToolCatalog{Tools: set.Catalog}); err != nil {
		serviceLog.Errorf("write chat tools response: %v", err)
	}
}

func (s *Service) handleChat(w http.ResponseWriter, request *http.Request) {
	var chat ChatRequest
	if err := json.NewDecoder(request.Body).Decode(&chat); err != nil {
		http.Error(w, fmt.Sprintf("invalid chat request: %v", err), http.StatusBadRequest)
		return
	}
	turnID, err := chatTurnID(chat)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	profile, err := s.runtimeProfile(request.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("load chat runtime profile: %v", err), http.StatusInternalServerError)
		return
	}
	if err := enforceRuntimeProfile(chat, profile.Resolved); err != nil {
		http.Error(w, err.Error(), requestErrorStatus(err))
		return
	}
	thread, err := s.resolveThreadSession(request.Context(), &chat)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateThreadTurn(chat, thread); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var attachments map[partLocation]api.AttachmentRef
	if chat.ToolApproval == nil {
		attachments, err = s.resolveAttachments(request.Context(), chat.Messages)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	resolved, err := requestSpec(chat, profile, attachments)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec := resolved.Spec
	set, err := s.loadTools(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	definitions, err := aitools.ResolveDefinitions(set.Definitions, spec.ToolPreferences)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Bound to this thread and appended after resolution: the conversation's own
	// name is not a preference the user manages. Chats that carry no caller tools
	// at all stay that way — they are named from their opening message instead.
	if chat.ThreadID != "" && len(definitions) > 0 && s.options.Threads != nil {
		definitions = append(definitions, s.sessionTitleTool(chat.ThreadID))
		appendSessionTitleInstruction(&spec)
	}
	config := profile.ProviderConfig
	config.Model = spec.Model
	config.Budget = spec.Budget
	config.SessionID = spec.SessionID
	config.CaptainSessionID = chat.ThreadID
	config.Tools = definitions
	config, err = s.prepareProviderConfig(request.Context(), config)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	spec.Model = config.Model
	resolved.Spec = spec
	var execution Execution
	var callerToolEvents <-chan api.Event
	if s.options.Authority != nil && chat.ThreadID != "" {
		title := ""
		if thread != nil {
			title = thread.Title
		}
		execution, err = s.options.Authority.Begin(request.Context(), ExecutionRequest{
			ThreadID: chat.ThreadID, RequestID: turnID, Title: title,
			Spec: spec, Profile: resolved, Definitions: definitions,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("admit chat execution: %v", err), http.StatusInternalServerError)
			return
		}
		defer closeExecution(execution)
		turnID = execution.TurnID()
		if chat.Trigger == "submit-message" && chat.MessageID == "" && len(chat.Messages) > 0 {
			chat.Messages[len(chat.Messages)-1].TurnID = turnID
		}
		config.CaptainSessionID = execution.CaptainSessionID()
		config.CallerTools = execution.CallerTools()
		if config.CallerTools != nil {
			callerToolEvents = execution.Events()
		}
	}
	if len(definitions) > 0 && isAgentBackend(config.Model.Backend) &&
		(execution == nil || config.CallerTools == nil) {
		http.Error(w, "agent caller tools require an authoritative Captain execution", http.StatusServiceUnavailable)
		return
	}
	provider, err := s.resolver.Provider(request.Context(), config)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer func() {
		if closeErr := closeProvider(provider); closeErr != nil {
			serviceLog.Errorf("close chat provider: %v", closeErr)
		}
	}()
	if len(definitions) > 0 {
		capability, ok := api.ProviderAs[api.ToolCapableProvider](provider)
		if !ok || !capability.SupportsCallerTools() {
			http.Error(w, fmt.Sprintf("backend %q does not support caller tools", provider.GetBackend()), http.StatusBadRequest)
			return
		}
	}
	if err := s.persistIncoming(request.Context(), chat); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	streamContext, cancel := context.WithCancel(request.Context())
	defer cancel()
	events, err := provider.ExecuteStream(streamContext, spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	events = mergeExecutionEvents(streamContext, events, callerToolEvents, definitions)
	if chat.ThreadID != "" {
		active := newActiveTurn(streamContext, provider, execution, cancel)
		if err := s.registerActiveTurn(chat.ThreadID, active); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		defer s.unregisterActiveTurn(chat.ThreadID, active)
		events = active.stream(events)
	}
	events = observeExecutionEvents(streamContext, execution, events)
	writer, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	costs := &TurnCosts{}
	persisted := s.persistedEvents(streamContext, persistedEventOptions{
		Request: chat, TurnID: turnID, Model: config.Model, Costs: costs,
	}, events)
	if err := WriteEventStream(writer, persisted, EventStreamOptions{
		ToolApproval: chat.ToolApproval,
		MessageID:    assistantMessageID(chat, turnID),
		Costs:        costs,
	}); err != nil {
		serviceLog.Errorf("stream chat response: %v", err)
	}
}

func assistantMessageID(request ChatRequest, turnID string) string {
	if request.MessageID != "" {
		return request.MessageID
	}
	if turnID != "" {
		return turnID + "-assistant"
	}
	return ""
}

func chatTurnID(request ChatRequest) (string, error) {
	if request.ThreadID == "" {
		return "", nil
	}
	if request.ID != request.ThreadID {
		return "", fmt.Errorf("chat id %q must match threadId %q", request.ID, request.ThreadID)
	}
	switch request.Trigger {
	case "submit-message":
		if request.MessageID != "" {
			return "", fmt.Errorf(
				"submit-message cannot include messageId %q; resolve approvals through /api/chat/sessions/{id}/approvals/{approvalID}",
				request.MessageID,
			)
		}
		if len(request.Messages) == 0 {
			return "", fmt.Errorf("submit-message requires a final user message")
		}
		last := request.Messages[len(request.Messages)-1]
		if !strings.EqualFold(last.Role, string(api.RoleUser)) {
			return "", fmt.Errorf("submit-message must end with a user message")
		}
		if last.ID == "" {
			return "", fmt.Errorf("submit-message final user message requires an id")
		}
		return last.ID, nil
	case "regenerate-message":
		if request.MessageID == "" {
			return "", fmt.Errorf("regenerate-message requires messageId")
		}
		return request.MessageID, nil
	default:
		return "", fmt.Errorf("unsupported chat trigger %q", request.Trigger)
	}
}

func validateThreadTurn(request ChatRequest, thread *Thread) error {
	if request.Trigger != "regenerate-message" || thread == nil {
		return nil
	}
	if len(thread.Messages) == 0 {
		return fmt.Errorf("regenerate-message messageId %q has no persisted assistant message", request.MessageID)
	}
	last := thread.Messages[len(thread.Messages)-1]
	if !strings.EqualFold(last.Role, string(api.RoleAssistant)) || last.ID != request.MessageID {
		return fmt.Errorf("regenerate-message messageId %q must match the final persisted assistant message", request.MessageID)
	}
	return nil
}

// runtimeProfile validates the server-owned profile before request fields are
// layered onto it, so profile defects remain server errors at the HTTP boundary.
func (s *Service) runtimeProfile(ctx context.Context) (RuntimeProfile, error) {
	if s.options.Profile == nil {
		return RuntimeProfile{}, nil
	}
	profile, err := s.options.Profile.RuntimeProfile(ctx)
	if err != nil {
		return RuntimeProfile{}, err
	}
	if len(profile.Resolved.Trace) == 0 {
		if !api.IsEmpty(profile.Resolved.Spec) || !api.IsEmpty(profile.Resolved.Constraints) {
			return RuntimeProfile{}, fmt.Errorf("chat runtime profile must include its resolution trace")
		}
		return profile, nil
	}
	resolved, err := api.ResolveSpecLayers(profile.Resolved.Trace...)
	if err != nil {
		return RuntimeProfile{}, fmt.Errorf("resolve chat runtime profile: %w", err)
	}
	profile.Resolved = resolved
	return profile, nil
}

func (s *Service) resolveThreadSession(ctx context.Context, request *ChatRequest) (*Thread, error) {
	if request.ThreadID == "" {
		return nil, nil
	}
	store, err := s.threads(ctx)
	if err != nil {
		return nil, err
	}
	thread, err := store.Get(ctx, request.ThreadID)
	if err != nil {
		return nil, err
	}
	if request.ProviderSessionID == "" {
		request.ProviderSessionID = thread.ProviderSessionID
	}
	return thread, nil
}

func closeExecution(execution Execution) {
	if execution == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := execution.Close(ctx); err != nil {
		serviceLog.Errorf("close authoritative chat execution: %v", err)
	}
}

func (s *Service) persistIncoming(ctx context.Context, request ChatRequest) error {
	if request.ThreadID == "" || request.Trigger != "submit-message" || request.MessageID != "" || len(request.Messages) == 0 {
		return nil
	}
	last := request.Messages[len(request.Messages)-1]
	if !strings.EqualFold(last.Role, string(api.RoleUser)) {
		return nil
	}
	store, err := s.threads(ctx)
	if err != nil {
		return err
	}
	if err := store.AppendMessage(ctx, request.ThreadID, last); err != nil {
		return err
	}
	// Names an as-yet-unnamed thread after the message that opened it. The store
	// keeps this from displacing a title the agent or the user already chose.
	s.setThreadTitle(ctx, request.ThreadID, TitleUpdate{
		Title: derivedTitle(request.Messages), Source: TitleSourceDerived,
	})
	return nil
}

// persistEvent accrues a completed turn against its thread. The thread returned
// by AddUsage carries the conversation's running total, which is recorded on
// costs so the finish part can report cumulative rather than per-turn spend.
func (s *Service) persistEvent(ctx context.Context, threadID string, event api.Event, model api.Model, costs *TurnCosts) error {
	store, err := s.threads(ctx)
	if err != nil {
		return err
	}
	if event.SessionID != "" {
		if err := store.SetProviderSession(ctx, threadID, event.SessionID); err != nil {
			return fmt.Errorf("persist provider session: %w", err)
		}
	}
	if event.Kind != api.EventResult || event.Usage == nil {
		return nil
	}
	thread, err := store.AddUsage(ctx, threadID, TurnUsage{
		InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens,
		ReasoningTokens: event.Usage.ReasoningTokens, CacheReadTokens: event.Usage.CacheReadTokens,
		CacheWriteTokens: event.Usage.CacheWriteTokens, CostUSD: event.CostUSD,
	})
	if err != nil {
		return fmt.Errorf("persist thread usage: %w", err)
	}
	if costs != nil {
		costs.Breakdown = costBreakdownMetadata(model, *event.Usage, event.CostUSD)
		if thread != nil {
			costs.ThreadCostUSD = thread.TotalCostUSD
		}
	}
	return nil
}

func costBreakdownMetadata(model api.Model, usage api.Usage, providerCostUSD float64) *CostBreakdownMetadata {
	cost := ai.PriceUsage(model.Backend, model.Name, usage, providerCostUSD)
	return &CostBreakdownMetadata{
		Model:        cost.Model,
		InputUSD:     cost.InputCost,
		OutputUSD:    cost.OutputCost,
		ReasoningUSD: cost.ReasoningCost,
		CacheReadUSD: cost.CacheReadCost,
		// genkit reports no cache-write tokens on the API backends, so this
		// stays zero there rather than being silently omitted.
		CacheWriteUSD: cost.CacheWriteCost,
		TotalUSD:      cost.Total(),
	}
}
