// Package session defines the unified session model: one canonical aggregate
// spanning cost, session/agent hierarchy, changed files, git state, plan events,
// and approvals, built from parsed Claude/Codex transcripts. Its message shape
// extends the Vercel AI SDK v6 UIMessage/UIPart (the format clicky/aichat and
// clicky-ui consume) with a transcript-provenance extension.
package session

import (
	"time"

	"github.com/segmentio/encoding/json"
)

// Part type discriminators. Tool parts are either the typed form "tool-<name>"
// or "dynamic-tool"; helpers below classify both.
const (
	PartText      = "text"
	PartReasoning = "reasoning"
	PartFile      = "file"
	PartTool      = "dynamic-tool"
)

// Tool-part state machine values (AI SDK v6).
const (
	ToolStateInputAvailable    = "input-available"
	ToolStateOutputAvailable   = "output-available"
	ToolStateApprovalRequested = "approval-requested"
	ToolStateApprovalResponded = "approval-responded"
	ToolStateOutputDenied      = "output-denied"
	ToolStateOutputError       = "output-error"
)

// Approval is the AI SDK v6 tool-approval envelope carried on a tool part.
// Approved is nil until the user responds.
type Approval struct {
	ID       string `json:"id"`
	Approved *bool  `json:"approved,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Provenance carries the transcript fields the replay shape has but the AI SDK
// UIPart lacks. It rides optionally on a Part/Message so a single canonical
// shape serves both the live-chat and JSONL-replay consumers.
type Provenance struct {
	Timestamp       *time.Time `json:"timestamp,omitempty"`
	CWD             string     `json:"cwd,omitempty"`
	Source          string     `json:"source,omitempty"` // "claude" | "codex"
	Model           string     `json:"model,omitempty"`
	ReasoningEffort string     `json:"reasoningEffort,omitempty"`
	GitBranch       string     `json:"gitBranch,omitempty"`
	UUID            string     `json:"uuid,omitempty"`
	ParentUUID      string     `json:"parentUuid,omitempty"`
	SessionID       string     `json:"sessionId,omitempty"`
	AgentID         string     `json:"agentId,omitempty"`
	APIErrorStatus  int        `json:"apiErrorStatus,omitempty"`
}

// Part is one content block of a Message: the union of text, reasoning, file
// (multimodal), and tool parts, matching aichat's UIPart.
type Part struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// file parts
	MediaType string `json:"mediaType,omitempty"`
	URL       string `json:"url,omitempty"`
	Filename  string `json:"filename,omitempty"`

	// tool parts
	ToolName   string          `json:"toolName,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	State      string          `json:"state,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Approval   *Approval       `json:"approval,omitempty"`
}

// Message is one message in a session, matching aichat's UIMessage plus an
// optional provenance extension. Raw retains the original JSONL line for
// internal source-aware processing; explicit raw history output is handled by
// the history row model. AgentID routes the message to its owning agent in the
// hierarchy and is not serialized (it is redundant with Provenance.AgentID).
type Message struct {
	ID         string          `json:"id,omitempty"`
	Role       string          `json:"role"`
	Parts      []Part          `json:"parts"`
	TurnID     string          `json:"turnId,omitempty"`
	Provenance *Provenance     `json:"provenance,omitempty"`
	Raw        json.RawMessage `json:"-"`
	// SourceLine is the 1-based JSONL line the message came from (0 when the
	// source format has no line mapping).
	SourceLine int64 `json:"sourceLine,omitempty"`

	AgentID string `json:"-"`
	// Provisional marks a message a later parse of a grown transcript can still
	// complete -- a tool call whose result had not been written yet, or a
	// reasoning span still open at EOF. Incremental ingest must keep re-offering
	// it instead of sealing the half-written row behind its high-water mark.
	Provisional bool `json:"-"`
}
