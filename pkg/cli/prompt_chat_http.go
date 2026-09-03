package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
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

func resumeSessionMessage(item SessionGetItem, request ChatMessageRequest) (ChatMessageResponse, error) {
	if strings.TrimSpace(request.Text) == "" {
		return ChatMessageResponse{}, newChatError(http.StatusBadRequest, "message text is required")
	}
	if strings.TrimSpace(item.ProviderSessionID) == "" {
		return ChatMessageResponse{}, newChatError(http.StatusUnprocessableEntity, "session has no provider session id")
	}
	if strings.TrimSpace(item.Summary.CWD) == "" {
		return ChatMessageResponse{}, newChatError(http.StatusUnprocessableEntity, "session has no working directory")
	}
	info, err := os.Stat(item.Summary.CWD)
	if err != nil || !info.IsDir() {
		return ChatMessageResponse{}, newChatError(http.StatusUnprocessableEntity, "session working directory is unavailable")
	}
	provider, err := resumeProvider(item.Summary.Source)
	if err != nil {
		return ChatMessageResponse{}, err
	}
	// A transcript names the agent that wrote it, never the mode it ran under, so
	// the mode is a deliberate choice here rather than a recovered fact: resume
	// goes to the agent mode, the only one that can continue a session mid-thread.
	mode := api.ModeAgent
	if request.Mode != "" && api.RuntimeMode(request.Mode) != mode {
		return ChatMessageResponse{}, newChatError(http.StatusUnprocessableEntity,
			fmt.Sprintf("session source %s must resume on the %s mode", item.Summary.Source, mode))
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = strings.TrimSpace(item.Summary.Model)
	}
	if model == "" {
		return ChatMessageResponse{}, newChatError(http.StatusUnprocessableEntity, "session has no model")
	}
	messageID := strings.TrimSpace(request.MessageID)
	if messageID == "" {
		messageID = newMessageID()
	}
	modelSpec := api.Model{
		Name: model, Provider: provider, Mode: mode, Effort: api.Effort(item.Summary.ReasoningEffort),
	}
	req := api.Spec{
		Model: modelSpec, Prompt: api.Prompt{User: strings.TrimSpace(request.Text)},
		SessionID: item.ProviderSessionID,
	}
	req.SetCwd(item.Summary.CWD)
	rendered := PromptRenderResult{
		Name: "resume " + item.CaptainID, Model: model, Provider: provider.Name, Mode: string(mode),
		User: req.Prompt.User, Input: req,
		Config: ai.Config{Model: modelSpec, SessionID: item.ProviderSessionID},
	}
	run, err := launchAsyncRun(item.CaptainID, rendered, true)
	if err != nil {
		return ChatMessageResponse{}, err
	}
	if stream, ok := promptRuns.get(run.RunID); ok {
		stream.publish(userChatMessage(messageID, req.Prompt.User))
	}
	return ChatMessageResponse{
		RunID: run.RunID, MessageID: messageID, Status: "started", Capabilities: run.Capabilities,
	}, nil
}

// resumeProvider maps a transcript source onto the provider that wrote it. The
// mode is NOT recoverable from a transcript — see resumeSessionMessage.
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
