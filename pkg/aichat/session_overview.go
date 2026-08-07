package aichat

import (
	"context"
	"sort"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

// OverviewProjectionStore is the read surface the cross-branch projection needs.
type OverviewProjectionStore interface {
	ListThreadSessionOverviews(context.Context, uuid.UUID) ([]database.SessionOverview, error)
	ListPlans(context.Context, database.PlanFilter) ([]database.Plan, error)
}

// ApplyOverviewProjection fills a session aggregate with everything that is a
// property of the stored row rather than of the branch that produced it: the
// monitor-owned metadata projection, the git blob, the context window, the
// authoritative plan, and the thread file rollup.
//
// Three branches build the same aggregate — the database branch, a transcript
// re-parse, and a prompt run — and each used to carry its own subset, so a
// session's changed files, git branch and plan appeared or vanished depending on
// which one served the request. Fields already set win: a transcript-derived
// value is fresher than the stored copy.
//
// Approvals are deliberately not projected. applyRequestState derives those from
// captain_turn_requests, where the stored transcript copy counts every
// operational tool use as an approval and reads as "200 approved".
func ApplyOverviewProjection(
	ctx context.Context,
	db OverviewProjectionStore,
	overview database.SessionOverview,
	detail *session.Session,
) error {
	applyOverviewMetadata(overview, detail)

	plans, err := db.ListPlans(ctx, database.PlanFilter{SourceSessionID: &overview.ID})
	if err != nil {
		return err
	}
	// captain_plans holds the approved revision, which the transcript copy does
	// not know about, so it outranks whatever the branch already found.
	if plan := planFromNative(plans); plan != nil {
		detail.Plan = plan
	}

	rootID := overview.ID
	if overview.RootSessionID != nil {
		rootID = *overview.RootSessionID
	}
	thread, err := db.ListThreadSessionOverviews(ctx, rootID)
	if err != nil {
		return err
	}
	detail.Files = threadFiles(overview.ID, detail.Files, thread)
	return nil
}

// applyOverviewMetadata fills the fields the stored row carries directly,
// leaving anything the branch already resolved alone.
func applyOverviewMetadata(overview database.SessionOverview, detail *session.Session) {
	metadata := session.DecodeMetadata(overview.Metadata)
	if detail.Model == "" {
		detail.Model = metadata.Model
	}
	if detail.Provider == "" {
		detail.Provider = metadata.Provider
	}
	if len(detail.Files.Read) == 0 && len(detail.Files.Written) == 0 {
		detail.Files = metadata.Files
	}
	if len(detail.Todos) == 0 {
		detail.Todos = metadata.Todos
	}
	if detail.Plan == nil {
		detail.Plan = metadata.Plan
	}
	if detail.Git == (session.GitState{}) {
		detail.Git = session.DecodeGitState(overview.Git)
	}
	if detail.Context == nil {
		detail.Context = overviewContext(overview)
	}
}

// overviewContext projects the context-window reading the overview already
// computes for the session list, so the detail view stops reporting none while
// the list row beside it reports a percentage.
func overviewContext(overview database.SessionOverview) *session.Context {
	if overview.ContextTokens == nil && overview.ContextWindowTokens == nil && overview.ContextFreePercent == nil {
		return nil
	}
	context := &session.Context{}
	if overview.ContextTokens != nil {
		context.UsedTokens = int(*overview.ContextTokens)
	}
	if overview.ContextWindowTokens != nil {
		context.WindowTokens = int(*overview.ContextWindowTokens)
	}
	if overview.ContextFreePercent != nil {
		context.FreePercent = *overview.ContextFreePercent
	}
	return context
}

// projectSessionAgents rebuilds the sub-agent hierarchy from the thread's agent
// rows, returning the root node and the flat index (root first) the transcript
// parser produces. Rows arrive root-first and parent-before-child from
// ListThreadAgents' ordering, but parentage is resolved through the index so a
// child whose parent is missing from the slice still lands in the flat list
// rather than disappearing.
func projectSessionAgents(rows []database.SessionAgent) (*session.Agent, []*session.Agent) {
	if len(rows) == 0 {
		return nil, nil
	}
	byID := make(map[string]*session.Agent, len(rows))
	agents := make([]*session.Agent, 0, len(rows))
	for _, row := range rows {
		agent := &session.Agent{
			ID: row.SessionID.String(), Type: stringPointer(row.AgentType), Desc: stringPointer(row.Description),
			IsRoot: row.IsRoot, HistoryFile: stringPointer(row.HistoryFile),
			Usage: api.Usage{
				InputTokens: int(row.InputTokens), OutputTokens: int(row.OutputTokens),
				ReasoningTokens: int(row.ReasoningTokens), CacheReadTokens: int(row.CacheReadTokens),
				CacheWriteTokens: int(row.CacheWriteTokens),
			},
			Cost: api.Cost{
				InputTokens: int(row.InputTokens), OutputTokens: int(row.OutputTokens),
				ReasoningTokens: int(row.ReasoningTokens), CacheReadTokens: int(row.CacheReadTokens),
				CacheWriteTokens: int(row.CacheWriteTokens), TotalTokens: int(row.TotalTokens),
				ProviderCostUSD: row.CostUSD,
			},
		}
		if row.ParentSessionID != nil {
			agent.ParentID = row.ParentSessionID.String()
		}
		byID[agent.ID] = agent
		agents = append(agents, agent)
	}

	var root *session.Agent
	for _, agent := range agents {
		if agent.IsRoot && root == nil {
			root = agent
		}
		if parent, ok := byID[agent.ParentID]; ok && agent.ParentID != agent.ID {
			parent.Children = append(parent.Children, agent)
		}
	}
	return root, agents
}

// threadFiles rolls a parent's changed files up from the sub-agents that did the
// work. A session that spawns sub-agents edits nothing itself — the row exists
// to own the thread — so reporting only its own (empty) set hides the whole
// thread's output. A leaf keeps its own set, or the hierarchy would stop
// distinguishing which sub-agent touched what.
func threadFiles(sessionID uuid.UUID, own session.ChangedFiles, thread []database.SessionOverview) session.ChangedFiles {
	children := map[uuid.UUID][]database.SessionOverview{}
	for _, row := range thread {
		if row.ParentSessionID != nil {
			children[*row.ParentSessionID] = append(children[*row.ParentSessionID], row)
		}
	}
	read, written := own.Read, own.Written
	descendants := 0
	for queue := children[sessionID]; len(queue) > 0; {
		row := queue[0]
		queue = append(queue[1:], children[row.ID]...)
		descendants++
		files := session.DecodeMetadata(row.Metadata).Files
		read, written = append(read, files.Read...), append(written, files.Written...)
	}
	if descendants == 0 {
		return own
	}
	return session.ChangedFiles{Read: sortedUnique(read), Written: sortedUnique(written)}
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok || value == "" {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

// planFromNative projects the authoritative plan for a session: the approved
// revision when one exists, otherwise the latest. Plans are ordered newest
// first, so the first approved row wins and an unapproved plan only supplies
// content when nothing was ever approved.
func planFromNative(plans []database.Plan) *session.Plan {
	var selected *database.Plan
	for i := range plans {
		if plans[i].ApprovedRevision != nil {
			selected = &plans[i]
			break
		}
		if selected == nil && plans[i].LatestRevision != nil {
			selected = &plans[i]
		}
	}
	if selected == nil {
		return nil
	}
	revision := selected.ApprovedRevision
	if revision == nil {
		revision = selected.LatestRevision
	}
	return &session.Plan{
		Path: selected.Path, Slug: selected.Slug, Content: revision.PlanMarkdown, Explicit: true,
	}
}
