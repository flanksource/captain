package cli

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky/task"
)

// sessionLatestMax bounds the latest line a session summary carries.
const sessionLatestMax = 200

// runSessionSummary is the running summary of the session a prompt run drives,
// as its task's SessionRef carries it: the stream folds every published entry,
// the model and the provider's context snapshots into it, and hands each change
// to notify together with the session id the task names.
type runSessionSummary struct {
	sessionID string
	summary   task.SessionSummary
	tools     map[string]bool
	notify    func(sessionID string, summary task.SessionSummary)
}

// bindTaskSession routes the run's session summary to notify, which names the
// task's session; nameTaskSession supplies the id it names.
func (s *runStream) bindTaskSession(notify func(sessionID string, summary task.SessionSummary)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session.notify = notify
}

// nameTaskSession records the session the run's task names, and names it with
// the summary so far. An empty id names nothing.
func (s *runStream) nameTaskSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID == "" {
		return
	}
	s.session.sessionID = sessionID
	s.notifySessionLocked()
}

// setSessionContext records the provider's latest context-window snapshot.
func (s *runStream) setSessionContext(usage api.ContextUsage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateSessionLocked(func(summary *task.SessionSummary) {
		summary.Context = &task.SessionContext{UsedTokens: usage.UsedTokens, WindowTokens: usage.WindowTokens}
	})
}

// summarizeEntryLocked counts the entry's new tool calls and takes its last
// tool or text part as the latest.
func (s *runStream) summarizeEntryLocked(entry session.Message) {
	s.updateSessionLocked(func(summary *task.SessionSummary) {
		for _, part := range entry.Parts {
			switch part.Type {
			case session.PartTool:
				call := firstNonEmpty(part.ToolCallID, entry.ID)
				if !s.session.tools[call] {
					s.session.tools[call] = true
					summary.Actions++
				}
				summary.Latest = &task.SessionLatest{Kind: "tool", Tool: part.ToolName, Text: toolInputSummary(part)}
			case session.PartText:
				if text := lastLine(part.Text); text != "" {
					summary.Latest = &task.SessionLatest{Kind: "text", Text: text}
				}
			}
		}
	})
}

// updateSessionLocked applies change and notifies when it changed anything.
func (s *runStream) updateSessionLocked(change func(*task.SessionSummary)) {
	if s.session.tools == nil {
		s.session.tools = map[string]bool{}
	}
	before := s.session.summary
	change(&s.session.summary)
	s.session.summary.Model = s.run.Model
	if !reflect.DeepEqual(before, s.session.summary) {
		s.notifySessionLocked()
	}
}

func (s *runStream) notifySessionLocked() {
	if s.session.notify == nil || s.session.sessionID == "" {
		return
	}
	s.session.notify(s.session.sessionID, s.session.summary)
}

// toolInputSummary is the one-line description of a tool call the session
// transcript renders for it, e.g. the file a Read opens.
func toolInputSummary(part session.Part) string {
	if len(part.Input) == 0 {
		return ""
	}
	var input map[string]any
	if err := json.Unmarshal(part.Input, &input); err != nil {
		log.Warnf("session summary: tool %s call %s input is not an object: %v", part.ToolName, part.ToolCallID, err)
		return ""
	}
	return history.Truncate(history.FormatToolUseSummary(part.ToolName, input), sessionLatestMax)
}

// lastLine is the last non-blank line of text, bounded to sessionLatestMax.
func lastLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[i+1:])
	}
	return history.Truncate(text, sessionLatestMax)
}
