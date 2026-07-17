// Package aichat implements the transport-facing AI SDK v6 chat protocol.
package aichat

import (
	"encoding/json"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

// ChatRequest is the body posted by the AI SDK DefaultChatTransport.
type ChatRequest struct {
	ID               string                  `json:"id,omitempty"`
	Messages         []UIMessage             `json:"messages"`
	Model            string                  `json:"model,omitempty"`
	ReasoningEffort  api.Effort              `json:"reasoningEffort,omitempty"`
	Temperature      *float64                `json:"temperature,omitempty"`
	Budget           api.Budget              `json:"budget,omitempty"`
	ToolPreferences  api.ToolPreferences     `json:"toolPreferences,omitempty"`
	PermissionMode   api.PermissionMode      `json:"permissionMode,omitempty"`
	ToolApproval    *api.ToolApprovalResume `json:"toolApproval,omitempty"`

	Context      string            `json:"context,omitempty"`
	ContextItems []ChatContextItem `json:"contextItems,omitempty"`

	ThreadID          string `json:"threadId,omitempty"`
	ProviderSessionID string `json:"providerSessionId,omitempty"`
}

// ChatContextItem carries app-owned structured state alongside its readable label.
type ChatContextItem struct {
	ID      string            `json:"id,omitempty"`
	Type    string            `json:"type,omitempty"`
	Label   string            `json:"label,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
	Payload json.RawMessage   `json:"payload,omitempty"`
}

// UIMessage is the AI SDK v6 message wire shape.
type UIMessage struct {
	ID       string           `json:"id,omitempty"`
	Role     string           `json:"role"`
	Parts    []UIPart         `json:"parts"`
	Metadata *MessageMetadata `json:"metadata,omitempty"`
}

// UIPart models the input part variants Captain consumes.
type UIPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	MediaType    string `json:"mediaType,omitempty"`
	URL          string `json:"url,omitempty"`
	Filename     string `json:"filename,omitempty"`
	AttachmentID string `json:"attachmentId,omitempty"`

	ToolName   string          `json:"toolName,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	State      string          `json:"state,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	ErrorText  string          `json:"errorText,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Approval   *Approval       `json:"approval,omitempty"`
}

// Approval is the AI SDK tool approval envelope attached to a tool UI part.
type Approval struct {
	ID       string `json:"id"`
	Approved *bool  `json:"approved,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// IsTool reports whether the part is a static or dynamic tool part.
func (p UIPart) IsTool() bool {
	return p.Type == "dynamic-tool" || strings.HasPrefix(p.Type, "tool-")
}

// EffectiveToolName returns a dynamic tool's explicit name or a static tool's type suffix.
func (p UIPart) EffectiveToolName() string {
	if p.ToolName != "" {
		return p.ToolName
	}
	name, ok := strings.CutPrefix(p.Type, "tool-")
	if !ok {
		return ""
	}
	return name
}

// ModelCatalogResponse reuses Captain's canonical frontend model catalog rows.
type ModelCatalogResponse = []ai.ModelInfo

// ModelCatalogEntry is Captain's canonical frontend model catalog row.
type ModelCatalogEntry = ai.ModelInfo

// ToolCatalogResponse reuses Captain's canonical frontend tool catalog.
type ToolCatalogResponse = tools.ToolCatalog

// ToolCatalogEntry is Captain's canonical frontend tool catalog row.
type ToolCatalogEntry = tools.ToolCatalogEntry
