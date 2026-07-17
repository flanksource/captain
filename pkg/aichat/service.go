package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
)

var serviceLog = logger.GetLogger("aichat")

// RuntimeSettings are application-owned defaults and provider construction
// settings evaluated for each request.
// RuntimeSettings is the request-scoped application configuration for a chat.
//
// The default model lives in Spec.Model — there is deliberately no DefaultModel
// string beside it. A bare name next to a structured Spec is the lossy pattern:
// it cannot carry a backend/mode/effort, so whatever it named got re-inferred, and
// when both were set they could silently disagree. Spec.Model can say
// {Name: "sol", Mode: ModeAgent} and mean it.
type RuntimeSettings struct {
	System              string
	Spec                api.Spec
	ProviderConfig      api.Config
	MaxInputTokens      int
	MonthlyTokenBudget  int
	CurrentMonthTokens  int
	MonthlyBudgetUSD    float64
	CurrentMonthCostUSD float64
}

// RuntimeSettingsProvider supplies request-scoped application settings.
type RuntimeSettingsProvider interface {
	RuntimeSettings(context.Context) (RuntimeSettings, error)
}

type RuntimeSettingsProviderFunc func(context.Context) (RuntimeSettings, error)

func (f RuntimeSettingsProviderFunc) RuntimeSettings(ctx context.Context) (RuntimeSettings, error) {
	return f(ctx)
}

// ServiceOptions injects every application-owned chat dependency. A nil
// Resolver uses Captain's canonical model/provider resolver.
type ServiceOptions struct {
	Resolver       Resolver
	ProviderConfig ProviderConfigSource
	Settings       RuntimeSettingsProvider
	Tools          ToolProvider
	MCP            ToolProvider
	Attachments    AttachmentResolver
	Threads        ThreadStore
}

// Service is Captain's AI SDK-compatible HTTP chat service.
type Service struct {
	options  ServiceOptions
	resolver Resolver
}

func NewService(options ServiceOptions) *Service {
	resolver := options.Resolver
	if resolver == nil {
		resolver = captainResolver{}
	}
	return &Service{options: options, resolver: resolver}
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("GET /api/chat/models", s.handleModels)
	mux.HandleFunc("GET /api/chat/tools", s.handleTools)
	s.registerThreadRoutes(mux)
	return mux
}

func (s *Service) handleModels(w http.ResponseWriter, request *http.Request) {
	models, err := s.resolver.Models(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := s.annotateConfiguredModels(request.Context(), models); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := writeJSON(w, http.StatusOK, models); err != nil {
		serviceLog.Errorf("write chat models response: %v", err)
	}
}

func (s *Service) handleTools(w http.ResponseWriter, request *http.Request) {
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
	settings, err := s.runtimeSettings(request.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("load chat runtime settings: %v", err), http.StatusInternalServerError)
		return
	}
	if err := enforceRuntimeSettings(chat, settings); err != nil {
		http.Error(w, err.Error(), requestErrorStatus(err))
		return
	}
	if err := s.resolveThreadSession(request.Context(), &chat); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	attachments, err := s.resolveAttachments(request.Context(), chat.Messages)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec, err := requestSpec(chat, settings, attachments)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	set, err := s.loadTools(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.persistIncoming(request.Context(), chat); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	config := settings.ProviderConfig
	config.Model = spec.Model
	config.Budget = spec.Budget
	config.SessionID = spec.SessionID
	config.Tools = set.Definitions
	provider, err := s.resolveProvider(request.Context(), config)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer func() {
		if closeErr := closeProvider(provider); closeErr != nil {
			serviceLog.Errorf("close chat provider: %v", closeErr)
		}
	}()
	if len(set.Definitions) > 0 {
		capability, ok := api.ProviderAs[api.ToolCapableProvider](provider)
		if !ok || !capability.SupportsCallerTools() {
			http.Error(w, fmt.Sprintf("backend %q does not support caller tools", provider.GetBackend()), http.StatusBadRequest)
			return
		}
	}
	streamContext, cancel := context.WithCancel(request.Context())
	defer cancel()
	events, err := provider.ExecuteStream(streamContext, spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writer, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := WriteEventStream(writer, s.persistedEvents(streamContext, chat, events)); err != nil {
		serviceLog.Errorf("stream chat response: %v", err)
	}
}

func (s *Service) runtimeSettings(ctx context.Context) (RuntimeSettings, error) {
	if s.options.Settings == nil {
		return RuntimeSettings{}, nil
	}
	return s.options.Settings.RuntimeSettings(ctx)
}

func (s *Service) resolveThreadSession(ctx context.Context, request *ChatRequest) error {
	if request.ThreadID == "" {
		return nil
	}
	if s.options.Threads == nil {
		return fmt.Errorf("thread persistence is not configured")
	}
	thread, err := s.options.Threads.Get(ctx, request.ThreadID)
	if err != nil {
		return err
	}
	if request.ProviderSessionID == "" {
		request.ProviderSessionID = thread.ProviderSessionID
	}
	return nil
}

func (s *Service) persistIncoming(ctx context.Context, request ChatRequest) error {
	if request.ThreadID == "" || len(request.Messages) == 0 {
		return nil
	}
	last := request.Messages[len(request.Messages)-1]
	if strings.EqualFold(last.Role, string(api.RoleUser)) {
		return s.options.Threads.AppendMessage(ctx, request.ThreadID, last)
	}
	return nil
}

func (s *Service) persistEvent(ctx context.Context, threadID string, event api.Event) error {
	if event.SessionID != "" {
		if err := s.options.Threads.SetProviderSession(ctx, threadID, event.SessionID); err != nil {
			return fmt.Errorf("persist provider session: %w", err)
		}
	}
	if event.Kind != api.EventResult || event.Usage == nil {
		return nil
	}
	_, err := s.options.Threads.AddUsage(ctx, threadID, TurnUsage{
		InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens,
		ReasoningTokens: event.Usage.ReasoningTokens, CacheReadTokens: event.Usage.CacheReadTokens,
		CacheWriteTokens: event.Usage.CacheWriteTokens, CostUSD: event.CostUSD,
	})
	if err != nil {
		return fmt.Errorf("persist thread usage: %w", err)
	}
	return nil
}
