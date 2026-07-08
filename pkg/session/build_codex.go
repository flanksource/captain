package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/commons/logger"
	"github.com/segmentio/encoding/json"
)

var codexLog = logger.GetLogger("session")

// BuildCodex builds the unified model for each given Codex session file. Codex
// sessions are flat (no sub-agent hierarchy), carry inline plan updates, and
// have no per-message token usage, so Cost is zero. Unreadable files are logged
// at Warn and skipped.
func BuildCodex(files []string) []*Session {
	out := make([]*Session, 0, len(files))
	for _, f := range files {
		uses, err := history.ExtractCodexToolUses(f)
		if err != nil {
			codexLog.Warnf("skipping unreadable codex session %s: %v", f, err)
			continue
		}
		if len(uses) == 0 {
			continue
		}
		info, _ := history.ReadCodexSessionInfo(f)
		s := buildCodexSession(uses, info)
		s.HistoryFile = f
		if s.Root != nil {
			s.Root.HistoryFile = f
		}
		out = append(out, s)
	}
	return out
}

// buildCodexSession assembles one Session from a Codex session's tool uses and
// metadata sidecar.
func buildCodexSession(uses []history.ToolUse, info *history.CodexSessionInfo) *Session {
	s := &Session{Source: "codex"}
	root := &Agent{IsRoot: true}

	var read, written []string
	for _, u := range uses {
		if s.ID == "" && u.SessionID != "" {
			s.ID = u.SessionID
		}
		if s.CWD == "" {
			s.CWD = u.CWD
		}
		if s.Model == "" {
			s.Model = u.Model
		}
		if u.Timestamp != nil {
			extendRange(s, *u.Timestamp)
		}
		if tools.IsEventToolName(u.Tool) || u.Tool == "ApiError" {
			s.Events = append(s.Events, codexUseToEvent(u))
			continue
		}
		collectCodexPaths(u, &read, &written)
		s.Messages = append(s.Messages, codexUseToMessage(u))
	}

	if info != nil {
		if s.ID == "" {
			s.ID = info.ID
		}
		if s.CWD == "" {
			s.CWD = info.CWD
		}
		s.Provider = info.ModelProvider
		s.Version = info.CLIVersion
		s.Git.Branch = info.GitBranch
		s.Git.Commit = info.GitCommit
		if s.Model == "" {
			s.Model = info.Model
		}
		if info.StartedAt != nil {
			extendRange(s, *info.StartedAt)
		}
	}

	root.ID = s.ID
	s.Root = root
	s.Agents = []*Agent{root}
	s.Files = ChangedFiles{
		Read:    sortedUnique(relativizeAll(read, s.CWD)),
		Written: sortedUnique(relativizeAll(written, s.CWD)),
	}
	s.Plan = CodexPlanFromToolUses(uses)
	return s
}

// CodexPlanFromToolUses renders the latest Codex update_plan/TodoWrite state as
// a canonical inline Plan. Codex revises plans in-place rather than writing plan
// files, so the final TodoWrite payload is the durable plan content.
func CodexPlanFromToolUses(uses []history.ToolUse) *Plan {
	var latest []any
	var ts *time.Time
	for _, use := range uses {
		if use.Tool != "TodoWrite" {
			continue
		}
		todos, ok := use.Input["todos"].([]any)
		if !ok || len(todos) == 0 {
			continue
		}
		latest = todos
		ts = use.Timestamp
	}
	if len(latest) == 0 {
		return nil
	}
	content := renderCodexPlan(latest)
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return &Plan{
		Content:  content,
		Explicit: true,
		Events:   []PlanEvent{{Kind: PlanWrite, Timestamp: ts}},
	}
}

func renderCodexPlan(steps []any) string {
	var b strings.Builder
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		text, _ := step["step"].(string)
		if text == "" {
			text, _ = step["content"].(string)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		status, _ := step["status"].(string)
		mark := " "
		suffix := ""
		switch status {
		case "completed", "done":
			mark = "x"
		case "in_progress":
			suffix = " _(in progress)_"
		}
		fmt.Fprintf(&b, "- [%s] %s%s\n", mark, text, suffix)
	}
	return strings.TrimRight(b.String(), "\n")
}

// relativizeAll makes paths relative to cwd for consistency with the claude
// model, which stores project-relative paths.
func relativizeAll(paths []string, cwd string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = claude.RelativePath(p, cwd)
	}
	return out
}

// codexUseToMessage maps a Codex tool use to a canonical message: "Assistant"
// and "Reasoning" become text/reasoning turns, everything else a tool part with
// the inline response as its output.
func codexUseToMessage(u history.ToolUse) Message {
	prov := &Provenance{
		Source:          "codex",
		CWD:             u.CWD,
		Model:           u.Model,
		ReasoningEffort: u.ReasoningEffort,
		SessionID:       u.SessionID,
		Timestamp:       u.Timestamp,
	}
	switch u.Tool {
	case "User":
		return Message{Role: "user", Parts: []Part{{Type: PartText, Text: codexText(u)}}, Provenance: prov}
	case "Assistant":
		return Message{Role: "assistant", Parts: []Part{{Type: PartText, Text: codexText(u)}}, Provenance: prov}
	case "Reasoning":
		return Message{Role: "assistant", Parts: []Part{{Type: PartReasoning, Text: codexText(u)}}, Provenance: prov}
	default:
		part := Part{
			Type:       PartTool,
			ToolName:   u.Tool,
			ToolCallID: u.ToolUseID,
			State:      ToolStateInputAvailable,
			Input:      marshalInput(u.Input),
		}
		if u.Response != "" {
			if out, err := json.Marshal(u.Response); err == nil {
				part.Output = out
			}
			part.State = ToolStateOutputAvailable
		}
		return Message{Role: "assistant", Parts: []Part{part}, Provenance: prov}
	}
}

func codexUseToEvent(u history.ToolUse) Event {
	typ, _ := u.Input["event"].(string)
	if typ == "" {
		typ = "event"
	}
	data := make(map[string]any, len(u.Input))
	for k, v := range u.Input {
		if k == "event" {
			continue
		}
		data[k] = v
	}
	return Event{
		Type:      typ,
		Scope:     "session",
		Timestamp: u.Timestamp,
		Data:      data,
	}
}

func codexText(u history.ToolUse) string {
	if t, ok := u.Input["text"].(string); ok {
		return t
	}
	return ""
}

func marshalInput(input map[string]any) json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	if b, err := json.Marshal(input); err == nil {
		return b
	}
	return nil
}

// collectCodexPaths appends read/write paths for file-touching Codex tools.
func collectCodexPaths(u history.ToolUse, read, written *[]string) {
	switch u.Tool {
	case "Read":
		if p, ok := u.Input["file_path"].(string); ok {
			*read = append(*read, p)
		}
	case "Grep", "Glob":
		if p, ok := u.Input["path"].(string); ok {
			*read = append(*read, p)
		}
	case "Write", "Edit":
		if p, ok := u.Input["file_path"].(string); ok {
			*written = append(*written, p)
		}
	}
}
