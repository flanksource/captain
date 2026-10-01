package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

func handlePromptRunMessage(chats *chatBroker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chat, ok := chats.getRun(r.PathValue("runId"))
		if !ok {
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		var request ChatMessageRequest
		if !decodeChatRequest(w, r, &request) {
			return
		}
		response, err := chat.send(r.Context(), request)
		if err != nil {
			writeChatError(w, err)
			return
		}
		writeChatJSON(w, http.StatusAccepted, response)
	}
}

func handlePromptRunInterrupt(chats *chatBroker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chat, ok := chats.getRun(r.PathValue("runId"))
		if !ok {
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		response, err := chat.interrupt(r.Context())
		if err != nil {
			writeChatError(w, err)
			return
		}
		writeChatJSON(w, http.StatusAccepted, response)
	}
}

func handlePromptRunPermissionMode(chats *chatBroker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chat, ok := chats.getRun(r.PathValue("runId"))
		if !ok {
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		var request ChatPermissionModeRequest
		if !decodeChatRequest(w, r, &request) {
			return
		}
		response, err := chat.setPermissionMode(r.Context(), request.Mode)
		if err != nil {
			writeChatError(w, err)
			return
		}
		writeChatJSON(w, http.StatusAccepted, response)
	}
}

func handlePromptRunStop(runs *runBroker, chats *chatBroker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runId")
		stream, ok := runs.get(runID)
		if !ok {
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		if chat, found := chats.getRun(runID); found {
			if !chat.stop() {
				writeChatError(w, newChatError(http.StatusConflict, "run is terminal"))
				return
			}
		} else if !stream.requestStop() {
			writeChatError(w, newChatError(http.StatusConflict, "run is terminal"))
			return
		}
		writeChatJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
	}
}

func handleSessionMessage(chats *chatBroker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request ChatMessageRequest
		if !decodeChatRequest(w, r, &request) {
			return
		}
		result, err := RunSessionGet(r.Context(), SessionGetOptions{ID: r.PathValue("id")})
		if err != nil {
			writeChatError(w, err)
			return
		}
		if result.Total == 0 {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		item, selectErr := selectResumeSession(result.Sessions)
		if selectErr != nil {
			writeChatError(w, selectErr)
			return
		}
		if chat, ok := chats.getSession(item.ProviderSessionID); ok {
			response, sendErr := chat.send(r.Context(), request)
			if sendErr != nil {
				writeChatError(w, sendErr)
				return
			}
			writeChatJSON(w, http.StatusAccepted, response)
			return
		}
		response, resumeErr := resumeSessionMessage(item, request)
		if resumeErr != nil {
			writeChatError(w, resumeErr)
			return
		}
		writeChatJSON(w, http.StatusAccepted, response)
	}
}

func selectResumeSession(items []SessionGetItem) (SessionGetItem, error) {
	if len(items) == 0 {
		return SessionGetItem{}, newChatError(http.StatusNotFound, "unknown session")
	}
	providerID := strings.TrimSpace(items[0].ProviderSessionID)
	selected := items[0]
	for _, item := range items[1:] {
		if providerID == "" || strings.TrimSpace(item.ProviderSessionID) != providerID {
			return SessionGetItem{}, newChatError(http.StatusConflict, "session id is ambiguous")
		}
		if resumeSessionScore(item) > resumeSessionScore(selected) {
			selected = item
		}
	}
	return selected, nil
}

func resumeSessionScore(item SessionGetItem) int {
	score := 0
	if strings.TrimSpace(item.Summary.Model) != "" {
		score += 4
	}
	if strings.TrimSpace(item.Summary.ModelMode) != "" {
		score += 2
	}
	if strings.TrimSpace(item.Summary.CWD) != "" {
		score += 2
	}
	if item.Detail != nil {
		score++
	}
	return score
}

// resumeMode is where a saved session continues. A transcript names the agent
// that wrote it, never the mode it ran under, so the mode is a deliberate choice
// rather than a recovered fact: the agent mode is the only one that can continue
// a session mid-thread.
const resumeMode = api.ModeAgent

func resumeSessionMessage(item SessionGetItem, request ChatMessageRequest) (ChatMessageResponse, error) {
	rendered, err := resumeRenderResult(item, request)
	if err != nil {
		return ChatMessageResponse{}, err
	}
	messageID := strings.TrimSpace(request.MessageID)
	if messageID == "" {
		messageID = newMessageID()
	}
	run, err := launchAsyncRun(item.CaptainID, rendered, true)
	if err != nil {
		return ChatMessageResponse{}, err
	}
	if stream, ok := promptRuns.get(run.RunID); ok {
		stream.publish(userChatMessage(messageID, rendered.User))
	}
	return ChatMessageResponse{
		RunID: run.RunID, MessageID: messageID, Status: "started", Capabilities: run.Capabilities,
	}, nil
}

// resumeRenderResult validates that a saved session can continue and renders
// the run that continues it.
func resumeRenderResult(item SessionGetItem, request ChatMessageRequest) (PromptRenderResult, error) {
	if strings.TrimSpace(request.Text) == "" {
		return PromptRenderResult{}, newChatError(http.StatusBadRequest, "message text is required")
	}
	if strings.TrimSpace(item.ProviderSessionID) == "" {
		return PromptRenderResult{}, newChatError(http.StatusUnprocessableEntity, "session has no provider session id")
	}
	target := resumeTargetOf(item)
	if strings.TrimSpace(target.CWD) == "" {
		return PromptRenderResult{}, newChatError(http.StatusUnprocessableEntity, "session has no working directory")
	}
	info, err := os.Stat(target.CWD)
	if err != nil || !info.IsDir() {
		return PromptRenderResult{}, newChatError(http.StatusUnprocessableEntity,
			fmt.Sprintf("session working directory %s is unavailable", target.CWD))
	}
	provider, err := resumeProvider(target.Source)
	if err != nil {
		return PromptRenderResult{}, err
	}
	if request.Mode != "" && api.RuntimeMode(request.Mode) != resumeMode {
		return PromptRenderResult{}, newChatError(http.StatusUnprocessableEntity,
			fmt.Sprintf("session source %s must resume on the %s mode", target.Source, resumeMode))
	}
	if request.PermissionMode != "" && !slices.Contains(api.SupportedPermissionModes(provider, resumeMode), request.PermissionMode) {
		return PromptRenderResult{}, newChatError(http.StatusUnprocessableEntity,
			fmt.Sprintf("permission mode %q is not supported by %s", request.PermissionMode, api.RuntimeOf(provider, resumeMode)))
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = strings.TrimSpace(target.Model)
	}
	if model == "" {
		return PromptRenderResult{}, newChatError(http.StatusUnprocessableEntity, "session has no model")
	}
	modelSpec := api.Model{
		Name: model, Provider: provider, Mode: resumeMode, Effort: api.Effort(item.Summary.ReasoningEffort),
	}
	req := api.Spec{
		Model: modelSpec, Prompt: api.Prompt{User: strings.TrimSpace(request.Text)},
		SessionID: item.ProviderSessionID, Permissions: api.Permissions{Mode: request.PermissionMode},
	}
	req.SetCwd(target.CWD)
	return PromptRenderResult{
		Name: "resume " + item.CaptainID, Model: model, Provider: provider.Name, Mode: string(resumeMode),
		User: req.Prompt.User, Input: req,
		Config: ai.Config{Model: modelSpec, SessionID: item.ProviderSessionID},
	}, nil
}

// sessionResumeTarget is what a saved session continues from: the provider
// that wrote its transcript, the directory that transcript ran in, and its model.
type sessionResumeTarget struct {
	Source, CWD, Model string
}

// resumeTargetOf reads the resume target from the transcript row a
// launcher-owned session executed in, and from the session itself otherwise. A
// launcher row names its launcher as the source and the repository root as its
// directory, neither of which can continue the provider's session.
func resumeTargetOf(item SessionGetItem) sessionResumeTarget {
	if item.Execution == nil {
		return sessionResumeTarget{Source: item.Summary.Source, CWD: item.Summary.CWD, Model: item.Summary.Model}
	}
	return sessionResumeTarget{
		Source: item.Execution.Source, CWD: item.Execution.CWD,
		Model: firstNonEmpty(item.Execution.Model, item.Summary.Model),
	}
}

// resumePermissionModes lists the postures a saved session from source may
// continue under; none when the source cannot be resumed.
func resumePermissionModes(source string) []api.PermissionMode {
	provider, err := resumeProvider(source)
	if err != nil {
		return nil
	}
	return api.SupportedPermissionModes(provider, resumeMode)
}

// resumeProvider maps a transcript source onto the provider that wrote it. The
// mode is NOT recoverable from a transcript — see resumeMode.
func resumeProvider(source string) (*api.ModelProvider, error) {
	for _, p := range api.Providers() {
		if p.AgentName == strings.ToLower(strings.TrimSpace(source)) {
			return p, nil
		}
	}
	return nil, newChatError(http.StatusUnprocessableEntity, "session source is not resumable")
}

func userChatMessage(messageID, text string) session.Message {
	return session.Message{
		ID: messageID, Role: "user",
		Parts: []session.Part{{Type: session.PartText, Text: text}},
	}
}

func newMessageID() string { return uuid.NewString() }

func decodeChatRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeChatError(w http.ResponseWriter, err error) {
	var target chatHTTPError
	if errors.As(err, &target) {
		http.Error(w, target.Error(), target.status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func writeChatJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Errorf("write chat response: %v", err)
	}
}
