package cli

import "github.com/flanksource/captain/pkg/api"

type ChatCapabilities struct {
	Interrupt bool `json:"interrupt"`
	Steer     bool `json:"steer"`
	FollowUp  bool `json:"followUp"`
	Resume    bool `json:"resume"`
}

type ChatQueuedMessage struct {
	MessageID string `json:"messageId"`
	Text      string `json:"text"`
}

type ChatStateFrame struct {
	RunID               string              `json:"runId"`
	SessionID           string              `json:"sessionId,omitempty"`
	Status              string              `json:"status"`
	Turn                int                 `json:"turn"`
	Capabilities        ChatCapabilities    `json:"capabilities"`
	Queued              []ChatQueuedMessage `json:"queued,omitempty"`
	DiscardedMessageIDs []string            `json:"discardedMessageIds,omitempty"`
	Summary             *PromptRunSummary   `json:"summary,omitempty"`
}

type PromptRunFrame struct {
	RunID        string           `json:"runId"`
	SessionID    string           `json:"sessionId,omitempty"`
	Status       string           `json:"status"`
	Chat         bool             `json:"chat"`
	Model        string           `json:"model,omitempty"`
	Backend      string           `json:"backend,omitempty"`
	Capabilities ChatCapabilities `json:"capabilities"`
}

type ChatMessageRequest struct {
	Text      string `json:"text"`
	MessageID string `json:"messageId,omitempty"`
	Model     string `json:"model,omitempty"`
	Backend   string `json:"backend,omitempty"`
}

type ChatMessageResponse struct {
	RunID        string           `json:"runId"`
	MessageID    string           `json:"messageId"`
	Status       string           `json:"status"`
	Capabilities ChatCapabilities `json:"capabilities"`
}

type ChatInterruptResponse struct {
	Status              string   `json:"status"`
	DiscardedMessageIDs []string `json:"discardedMessageIds,omitempty"`
}

func chatCapabilitiesForBackend(backend string) ChatCapabilities {
	switch api.Backend(backend) {
	case api.BackendClaudeAgent:
		return ChatCapabilities{Interrupt: true, Steer: true, FollowUp: true, Resume: true}
	case api.BackendCodexAgent:
		return ChatCapabilities{Interrupt: true, FollowUp: true, Resume: true}
	case api.BackendClaudeCLI, api.BackendCodexCLI, api.BackendClaudeCmux, api.BackendCodexCmux:
		return ChatCapabilities{Resume: true}
	default:
		return ChatCapabilities{}
	}
}
