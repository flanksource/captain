package session

import (
	"fmt"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
)

// TranscriptInfo identifies one transcript file relative to its session: the
// root session id derived from the path/content and, for sub-agent transcripts,
// the agent identity that becomes a child session.
type TranscriptInfo struct {
	Path          string
	RootSessionID string
	IsAgent       bool
	AgentID       string
	AgentType     string
	AgentDesc     string
}

// BuildTranscriptFile builds the unified model for a single Claude transcript
// file (root or sub-agent) without directory discovery, for ingest pipelines
// that already know exactly which file changed.
func BuildTranscriptFile(path string) (*Session, TranscriptInfo, error) {
	t, rootID, err := claude.ParseTranscript(path)
	if err != nil {
		return nil, TranscriptInfo{}, err
	}
	if len(t.Entries) == 0 {
		return nil, TranscriptInfo{}, fmt.Errorf("transcript %s has no entries", path)
	}
	info := TranscriptInfo{
		Path: path, RootSessionID: rootID,
		IsAgent: t.IsAgent, AgentID: t.AgentID, AgentType: t.AgentType, AgentDesc: t.AgentDesc,
	}
	ps := claude.ParsedSession{SessionID: rootID, Transcripts: []claude.ParsedTranscript{t}}
	return buildSession(ps), info, nil
}

// IsSyntheticEventTool reports whether a tool part name is a synthetic
// event/lifecycle projection (session init, hooks, result summaries, API/parse
// errors) rather than conversational content. Messages whose parts are all
// synthetic are transcript events, not messages.
func IsSyntheticEventTool(name string) bool {
	if name == "" {
		return false
	}
	if tools.IsEventToolName(name) {
		return true
	}
	switch name {
	case "ApiError", "ParseError", "Result", "SessionInit", "HookStart", "HookResponse",
		"StopHookSummary", "TurnDuration", "AwaySummary", "SessionTitle", "CompactBoundary",
		"LocalCommand", "ScheduledTaskFire", "Informational", "PrLink", "WorktreeState",
		"Relocated", "Started", "Event":
		return true
	default:
		return false
	}
}

// IsConversationalMessage reports whether a message carries conversational
// content worth persisting as a transcript message: any text/reasoning/file
// part, or a tool part that is a real tool call rather than a synthetic event.
func IsConversationalMessage(m Message) bool {
	for _, p := range m.Parts {
		switch p.Type {
		case PartText, PartReasoning, PartFile:
			return true
		default:
			if !IsSyntheticEventTool(p.ToolName) {
				return true
			}
		}
	}
	return false
}
