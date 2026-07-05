package session

import (
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

// Build discovers the in-scope sessions and returns the unified model for each,
// including sub-agent transcripts when filter.IncludeAgents. Cost is always
// computed (no --claude/--codex gating), so every caller gets token/cost data.
func Build(currentDir string, searchAll bool, filter claude.Filter) ([]*Session, error) {
	parsed, err := claude.ParseSessions(currentDir, searchAll, filter)
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(parsed))
	for _, ps := range parsed {
		out = append(out, buildSession(ps))
	}
	return out, nil
}

// buildSession assembles one Session from a parsed root+sub-agent group.
func buildSession(ps claude.ParsedSession) *Session {
	h := buildHierarchy(ps)

	s := &Session{
		ID:       ps.SessionID,
		Source:   "claude",
		Root:     h.root,
		Agents:   h.agents,
		Messages: h.messages,
	}

	var allEntries []claude.HistoryEntry
	var allToolUses []claude.ToolUse
	costs := api.Costs{}
	for _, t := range ps.Transcripts {
		allEntries = append(allEntries, t.Entries...)
		allToolUses = append(allToolUses, t.ToolUses...)
	}

	applyMetadata(s, ps, allEntries)
	for _, e := range allEntries {
		if e.IsAssistantMessage() && e.Message.Usage != nil {
			costs = append(costs, CostFromUsage(e.Message.Usage, e.Message.Model))
		}
	}
	s.Cost = costs.Sum()
	s.Usage = usageFromCost(s.Cost)
	s.ToolCosts = collapseByModel(costs)

	s.Files = changedFiles(allToolUses)
	s.Plan = buildPlan(allEntries, allToolUses)
	s.Approvals = approvalStats(allToolUses)
	return s
}

// applyMetadata fills session identity/range fields from the transcript entries.
func applyMetadata(s *Session, ps claude.ParsedSession, entries []claude.HistoryEntry) {
	if len(ps.Transcripts) > 0 {
		projectPath := claude.ExtractProjectPath(ps.Transcripts[0].Path)
		s.Project = filepath.Base(claude.FindProjectRoot(projectPath))
	}
	for _, e := range entries {
		if e.SessionID != "" && s.ID == "" {
			s.ID = e.SessionID
		}
		if s.CWD == "" {
			s.CWD = e.CWD
		}
		if s.Git.Branch == "" {
			s.Git.Branch = e.GitBranch
		}
		if s.Version == "" {
			s.Version = e.Version
		}
		if s.Slug == "" {
			s.Slug = e.Slug
		}
		if s.Model == "" && e.Message.Model != "" {
			s.Model = e.Message.Model
		}
		if ts, err := e.ParseTimestamp(); err == nil {
			extendRange(s, ts)
		}
	}
}

func extendRange(s *Session, ts time.Time) {
	if ts.IsZero() {
		return
	}
	if s.StartedAt == nil || ts.Before(*s.StartedAt) {
		t := ts
		s.StartedAt = &t
	}
	if s.EndedAt == nil || ts.After(*s.EndedAt) {
		t := ts
		s.EndedAt = &t
	}
}

// collapseByModel groups per-call costs by model into a stable slice.
func collapseByModel(costs api.Costs) api.Costs {
	byModel := costs.ByModel()
	out := make(api.Costs, 0, len(byModel))
	for model, c := range byModel {
		c.Model = model
		out = append(out, c)
	}
	return out
}

// changedFiles aggregates read/write paths across a session's tool uses,
// distinguishing reads from writes.
func changedFiles(uses []claude.ToolUse) ChangedFiles {
	var read, written []string
	for _, tu := range uses {
		switch tu.Tool {
		case "Read":
			read = append(read, relPath(tu, tu.FilePath()))
		case "Grep", "Glob":
			if p, ok := tu.Input["path"].(string); ok {
				read = append(read, relPath(tu, p))
			}
		case "Write", "Edit":
			written = append(written, relPath(tu, tu.FilePath()))
		}
	}
	return ChangedFiles{Read: sortedUnique(read), Written: sortedUnique(written)}
}

func relPath(tu claude.ToolUse, path string) string {
	if path == "" {
		return ""
	}
	return claude.RelativePath(path, tu.ProjectRoot)
}

// buildPlan recovers the session plan and its lifecycle events.
func buildPlan(entries []claude.HistoryEntry, uses []claude.ToolUse) *Plan {
	sp := claude.PlanFromEntries(entries)
	if sp == nil {
		return nil
	}
	plan := &Plan{Path: sp.Path, Slug: sp.Slug, Content: sp.Content, Explicit: sp.Explicit}
	for _, tu := range uses {
		if tu.Tool != "ExitPlanMode" {
			continue
		}
		kind := PlanExit
		reason := ""
		if tu.Denied {
			kind = PlanDenied
			reason = tu.DeniedReason
		}
		plan.Events = append(plan.Events, PlanEvent{Kind: kind, Timestamp: tu.Timestamp, Reason: reason})
	}
	return plan
}

// approvalStats counts approvals/denials across the session's tool uses.
func approvalStats(uses []claude.ToolUse) ApprovalStats {
	var stats ApprovalStats
	for _, tu := range uses {
		switch {
		case tu.Denied:
			stats.Denied++
			stats.Denials = append(stats.Denials, Denial{
				ToolUseID: tu.ToolUseID,
				Tool:      tu.Tool,
				Reason:    tu.DeniedReason,
			})
		case tu.Tool == "ExitPlanMode" || tu.Tool == "User":
			// plan/synthetic rows are not approvals
		default:
			stats.Approved++
		}
	}
	return stats
}
