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
	Provider     string           `json:"provider,omitempty"`
	Mode         string           `json:"mode,omitempty"`
	Capabilities ChatCapabilities `json:"capabilities"`
}

type ChatMessageRequest struct {
	Text      string `json:"text"`
	MessageID string `json:"messageId,omitempty"`
	Model     string `json:"model,omitempty"`
	Mode      string `json:"mode,omitempty"`
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

// chatCapabilitiesForRuntime reads the provider×mode cell rather than restating
// it. This was an eleven-way switch that duplicated
// registry.ModeCapabilities{Streaming,Resume,Interrupt,Steer} and could drift
// from the adapters it described.
func chatCapabilitiesForRuntime(provider *api.ModelProvider, mode api.RuntimeMode) ChatCapabilities {
	if provider == nil {
		return ChatCapabilities{}
	}
	caps, known := provider.Caps(mode)
	if !known {
		return ChatCapabilities{}
	}
	return ChatCapabilities{
		Interrupt: caps.Interrupt,
		Steer:     caps.Steer,
		// A follow-up turn needs an interruptible, resumable session: the local
		// transports that only resume can continue, but not mid-turn.
		FollowUp: caps.Interrupt && caps.Resume,
		Resume:   caps.Resume,
	}
}

// chatCapabilitiesFor resolves the runtime from a rendered result's provider and
// mode strings.
func chatCapabilitiesFor(provider, mode string) ChatCapabilities {
	p, _ := api.ProviderByName(provider)
	return chatCapabilitiesForRuntime(p, api.RuntimeMode(mode))
}
