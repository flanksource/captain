package session

import "github.com/flanksource/captain/pkg/api"

// Where an AwaitingInput's questions came from.
const (
	// AwaitingInputEnvelope is a result envelope that ended the run on ask.
	AwaitingInputEnvelope = "envelope"
	// AwaitingInputAskUserQuestion is a native AskUserQuestion the run could not
	// put to anyone.
	AwaitingInputAskUserQuestion = "ask-user-question"
)

// AwaitingInput is what a session's last turn left for a person to answer
// before the agent can continue. It is derived from the transcript on read and
// is nil once anything after the question answered it.
type AwaitingInput struct {
	Origin    string                 `json:"origin"`
	Summary   string                 `json:"summary,omitempty"`
	Questions []api.TerminalQuestion `json:"questions"`
	// MessageID and ToolCallID locate the question in the transcript.
	MessageID  string `json:"messageId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}
