package session

import (
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/commons/logger"
	"github.com/segmentio/encoding/json"
)

var codexLog = logger.GetLogger("session")

// BuildCodex builds the unified model for each given Codex session file. Codex
// sessions are flat (no sub-agent hierarchy), carry no plan/approval data, and
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
		out = append(out, buildCodexSession(uses, info))
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
		if s.StartedAt == nil && info.StartedAt != nil {
			s.StartedAt = info.StartedAt
		}
	}

	root.ID = s.ID
	s.Root = root
	s.Agents = []*Agent{root}
	s.Files = ChangedFiles{
		Read:    sortedUnique(relativizeAll(read, s.CWD)),
		Written: sortedUnique(relativizeAll(written, s.CWD)),
	}
	return s
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
