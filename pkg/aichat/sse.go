package aichat

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Part is one AI SDK v6 UI Message Stream chunk.
type Part struct {
	Type string `json:"type"`

	MessageID string `json:"messageId,omitempty"`
	ID        string `json:"id,omitempty"`
	Delta     string `json:"delta,omitempty"`

	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	Input      any    `json:"input,omitempty"`
	Output     any    `json:"output,omitempty"`
	Dynamic    bool   `json:"dynamic,omitempty"`
	ApprovalID string `json:"approvalId,omitempty"`

	Data            any              `json:"data,omitempty"`
	ErrorText       string           `json:"errorText,omitempty"`
	MessageMetadata *MessageMetadata `json:"messageMetadata,omitempty"`
}

// UsageMetadata is Captain usage normalized for frontend message metadata.
type UsageMetadata struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	ReasoningTokens  int `json:"reasoningTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// CostBreakdownMetadata is one turn's cost split across the disjoint token
// buckets, priced by ai.PriceUsage. Field names match the frontend's
// ChatCostBreakdown so the UI renders per-bucket rows instead of "-".
type CostBreakdownMetadata struct {
	Model        string  `json:"model,omitempty"`
	InputUSD     float64 `json:"inputUsd"`
	OutputUSD    float64 `json:"outputUsd"`
	ReasoningUSD float64 `json:"reasoningUsd"`
	CacheReadUSD float64 `json:"cacheReadUsd"`
	// CacheWriteUSD is structurally zero on the API backends: genkit's usage
	// type carries no cache-write field (pkg/ai/provider/genkit/mapping.go).
	CacheWriteUSD float64 `json:"cacheWriteUsd"`
	TotalUSD      float64 `json:"totalUsd"`
}

// MessageMetadata is attached to the assistant UIMessage by the finish part.
type MessageMetadata struct {
	ProviderSessionID string                 `json:"providerSessionId,omitempty"`
	Model             string                 `json:"model,omitempty"`
	Usage             *UsageMetadata         `json:"usage,omitempty"`
	Cost              float64                `json:"cost,omitempty"`
	CostBreakdown     *CostBreakdownMetadata `json:"costBreakdown,omitempty"`
	// ThreadCostUSD is the conversation's cumulative spend. Cost above is this
	// turn alone; a UI showing a running total must read this field, since the
	// two differ by the number of turns taken.
	ThreadCostUSD float64 `json:"threadCostUsd,omitempty"`
	ContextTokens int     `json:"contextTokens,omitempty"`
	Success       *bool   `json:"success,omitempty"`
	Interrupted   bool    `json:"interrupted,omitempty"`
}

// TurnCosts is written by the persistence layer as a turn completes and read by
// the event stream when it writes the finish part. The event channel between
// them orders the write before the read.
type TurnCosts struct {
	Breakdown     *CostBreakdownMetadata
	ThreadCostUSD float64
}

// SSEWriter writes AI SDK v6 chunks using Server-Sent Events framing.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	done    bool
}

// NewSSEWriter initializes a flushable HTTP response for the UI Message Stream protocol.
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	headers := w.Header()
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("x-vercel-ai-ui-message-stream", "v1")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("x-accel-buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &SSEWriter{w: w, flusher: flusher}, nil
}

// WritePart writes and flushes one JSON chunk.
func (w *SSEWriter) WritePart(part Part) error {
	if w.done {
		return fmt.Errorf("AI SDK stream is already done")
	}
	if part.Type == "" {
		return fmt.Errorf("AI SDK stream part type is required")
	}
	payload, err := json.Marshal(part)
	if err != nil {
		return fmt.Errorf("marshal AI SDK stream part %q: %w", part.Type, err)
	}
	if _, err := fmt.Fprintf(w.w, "data: %s\n\n", payload); err != nil {
		return fmt.Errorf("write AI SDK stream part %q: %w", part.Type, err)
	}
	w.flusher.Flush()
	return nil
}

// Done writes and flushes the literal stream terminator.
func (w *SSEWriter) Done() error {
	if w.done {
		return fmt.Errorf("AI SDK stream is already done")
	}
	w.done = true
	if _, err := fmt.Fprint(w.w, "data: [DONE]\n\n"); err != nil {
		return fmt.Errorf("write AI SDK stream terminator: %w", err)
	}
	w.flusher.Flush()
	return nil
}
