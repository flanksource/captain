package session

import (
	"time"

	"github.com/segmentio/encoding/json"
)

type Request struct {
	ID           string          `json:"id"`
	TurnID       string          `json:"turnId,omitempty"`
	PromptRunID  string          `json:"promptRunId,omitempty"`
	ModelCallID  string          `json:"modelCallId,omitempty"`
	ToolCallID   string          `json:"toolCallId,omitempty"`
	Kind         string          `json:"kind"`
	State        string          `json:"state"`
	Tool         string          `json:"tool,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	RequestedBy  string          `json:"requestedBy,omitempty"`
	ResolvedBy   string          `json:"resolvedBy,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	Version      int64           `json:"version"`
	ExpiresAt    *time.Time      `json:"expiresAt,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	ResolvedAt   *time.Time      `json:"resolvedAt,omitempty"`
}
