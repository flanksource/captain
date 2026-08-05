package session

import (
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

// RowsFromFile builds the store rows for a single transcript file: the one row
// for a Codex session, or the root/sub-agent row for one Claude transcript
// (parent linked to its root session by path). Used by the summary cache to
// (re)build one file on a miss.
func RowsFromFile(path, source string) ([]Row, error) {
	if source == "codex" {
		if r, ok := CodexRow(path); ok {
			return []Row{r}, nil
		}
		return nil, fmt.Errorf("codex session %q has no tool uses", path)
	}
	t, sessionID, err := claude.ParseTranscript(path)
	if err != nil {
		return nil, err
	}
	return Rows(claude.ParsedSession{SessionID: sessionID, Transcripts: []claude.ParsedTranscript{t}}), nil
}

// Row is the per-transcript projection of the unified model for the persistent
// session store: one Row per session (root) or sub-agent, linked by ParentID.
// It carries the rich metadata (git, cost, changed files, plan/approvals,
// prompt refs) but not the message stream.
type Row struct {
	Path      string
	ID        string
	ParentID  string // "" for a root session
	Source    string // "claude" | "codex"
	IsAgent   bool
	AgentType string
	AgentDesc string

	Project         string
	CWD             string
	Version         string
	Model           string
	Provider        string
	ReasoningEffort string
	Title           string
	InitialPrompt   string
	Git             GitState
	StartedAt       *time.Time
	EndedAt         *time.Time

	Cost      api.Cost
	Usage     api.Usage
	Files     ChangedFiles
	Approvals ApprovalStats
	ToolCalls int
	Messages  int

	// ContextTokens is the last-turn context occupancy (input + cache tokens of
	// the final assistant message) — the basis for the context-free-% signal.
	ContextTokens int

	Slug string
	Plan *Plan
}

// Rows projects a parsed Claude session into one Row per transcript — the root
// plus each sub-agent — with ParentID resolved from parentUuid (falling back to
// the root session), so sub-agents persist as their own rows linked to a parent.
func Rows(ps claude.ParsedSession) []Row {
	uuidOwner := map[string]string{}
	for _, t := range ps.Transcripts {
		owner := ""
		if t.IsAgent {
			owner = t.AgentID
		}
		for _, e := range t.Entries {
			if e.UUID != "" {
				uuidOwner[e.UUID] = owner
			}
		}
	}
	out := make([]Row, 0, len(ps.Transcripts))
	for _, t := range ps.Transcripts {
		out = append(out, rowFromTranscript(ps.SessionID, t, uuidOwner))
	}
	return out
}

func rowFromTranscript(sessionID string, t claude.ParsedTranscript, uuidOwner map[string]string) Row {
	r := Row{
		Path:      t.Path,
		Source:    "claude",
		IsAgent:   t.IsAgent,
		AgentType: t.AgentType,
		AgentDesc: t.AgentDesc,
	}
	if t.IsAgent {
		r.ID = t.AgentID
		r.ParentID = resolveParent(t, uuidOwner, sessionID)
	} else {
		r.ID = sessionID
	}

	costs := newResponseCosts()
	for _, e := range t.Entries {
		if costs.firstSighting(e) {
			costs.costs = append(costs.costs, CostFromUsage(e.Message.Usage, e.Message.Model))
			u := e.Message.Usage
			r.ContextTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		}
		if r.CWD == "" {
			r.CWD = e.CWD
		}
		if r.Git.Branch == "" {
			r.Git.Branch = e.GitBranch
		}
		if r.Version == "" {
			r.Version = e.Version
		}
		if r.Model == "" && e.Message.Model != "" {
			r.Model = e.Message.Model
		}
		if r.Slug == "" {
			r.Slug = e.Slug
		}
		if ts, err := e.ParseTimestamp(); err == nil {
			extendRowRange(&r, ts)
		}
	}
	r.Cost = costs.costs.Sum()
	if !t.IsAgent {
		r.Title = latestClaudeSessionTitle(t.ToolUses)
		r.InitialPrompt = firstClaudeUserPrompt(t.Entries)
		if r.Title == "" {
			r.Title = r.Slug
		}
		if r.Title == "" {
			r.Title = deriveSessionTitle(r.InitialPrompt)
		}
	}
	r.Usage = usageFromCost(r.Cost)
	r.Files = changedFiles(t.ToolUses)
	r.Approvals = approvalStats(t.ToolUses)
	r.ToolCalls = countOperationalToolUses(t.ToolUses)
	r.Messages = countEntryMessages(t.Entries)
	if !t.IsAgent {
		r.Plan = buildPlan(t.Entries, t.ToolUses)
	}
	return r
}

// CodexRow builds a single Row for a Codex session file (Codex is flat — no
// sub-agent hierarchy). Returns ok=false for an unreadable/empty session.
func CodexRow(path string) (Row, bool) {
	uses, err := history.ExtractCodexToolUses(path)
	if err != nil || len(uses) == 0 {
		return Row{}, false
	}
	info, _ := history.ReadCodexSessionInfo(path)
	s := buildCodexSession(uses, info)
	r := Row{
		Path:          path,
		ID:            s.ID,
		Source:        "codex",
		Project:       s.Project,
		CWD:           s.CWD,
		Version:       s.Version,
		Model:         s.Model,
		Provider:      s.Provider,
		Title:         s.Title,
		InitialPrompt: s.InitialPrompt,
		Git:           s.Git,
		StartedAt:     s.StartedAt,
		EndedAt:       s.EndedAt,
		Cost:          s.Cost,
		Usage:         s.Usage,
		Files:         s.Files,
		Slug:          s.Slug,
		Plan:          s.Plan,
	}
	if info != nil {
		r.ReasoningEffort = info.ReasoningEffort
	}
	for _, u := range uses {
		if isChatActivity(u.Tool) {
			r.Messages++
		} else if isOperationalToolActivity(u.Tool) {
			r.ToolCalls++
		}
	}
	return r, true
}

func countOperationalToolUses(uses []claude.ToolUse) int {
	n := 0
	for _, use := range uses {
		if isOperationalToolActivity(use.Tool) {
			n++
		}
	}
	return n
}

func extendRowRange(r *Row, ts time.Time) {
	if ts.IsZero() {
		return
	}
	if r.StartedAt == nil || ts.Before(*r.StartedAt) {
		t := ts
		r.StartedAt = &t
	}
	if r.EndedAt == nil || ts.After(*r.EndedAt) {
		t := ts
		r.EndedAt = &t
	}
}

// countEntryMessages counts user/assistant entries that carry text or reasoning
// content (the "message" notion the session summary uses — tool-only entries
// don't count).
func countEntryMessages(entries []claude.HistoryEntry) int {
	n := 0
	for _, e := range entries {
		if e.Message.Role != claude.MessageRoleUser && e.Message.Role != claude.MessageRoleAssistant {
			continue
		}
		if hasTextContent(e) {
			n++
		}
	}
	return n
}

func hasTextContent(e claude.HistoryEntry) bool {
	for _, b := range e.Message.Content {
		switch b.Type {
		case claude.ContentTypeText:
			if b.Text != "" {
				return true
			}
		case claude.ContentTypeThinking, claude.ContentTypeRedactedThinking:
			if b.Thinking != "" {
				return true
			}
		}
	}
	return false
}
