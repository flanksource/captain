package query

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

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

func descendantFiles(sessionID uuid.UUID, thread []database.SessionOverview) *session.ChangedFiles {
	children := map[uuid.UUID][]database.SessionOverview{}
	for _, row := range thread {
		if row.ParentSessionID != nil {
			children[*row.ParentSessionID] = append(children[*row.ParentSessionID], row)
		}
	}
	var read, written []string
	descendants := 0
	for queue := children[sessionID]; len(queue) > 0; {
		row := queue[0]
		queue = append(queue[1:], children[row.ID]...)
		descendants++
		files := session.DecodeMetadata(row.Metadata).Files
		read = append(read, files.Read...)
		written = append(written, files.Written...)
	}
	if descendants == 0 {
		return nil
	}
	return &session.ChangedFiles{Read: sortedUnique(read), Written: sortedUnique(written)}
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, found := seen[value]; found || value == "" {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

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
	return &session.Plan{Path: selected.Path, Slug: selected.Slug, Content: revision.PlanMarkdown, Explicit: true}
}

func projectSessionRequests(rows []database.TurnRequest) ([]session.Request, error) {
	requests := make([]session.Request, len(rows))
	for i := range rows {
		input, err := json.Marshal(rows[i].Request["input"])
		if err != nil {
			return nil, fmt.Errorf("encode Captain request %s input: %w", rows[i].ID, err)
		}
		updatedInput, err := json.Marshal(rows[i].Response["updatedInput"])
		if err != nil {
			return nil, fmt.Errorf("encode Captain request %s updated input: %w", rows[i].ID, err)
		}
		requests[i] = session.Request{
			ID: rows[i].ID.String(), ToolCallID: rows[i].ToolCallID, Kind: rows[i].Kind, State: string(rows[i].State),
			Tool: fmt.Sprint(rows[i].Request["tool"]), Input: input, RequestedBy: rows[i].RequestedBy,
			ResolvedBy: rows[i].ResolvedBy, Reason: rows[i].Reason, Version: rows[i].Version,
			ExpiresAt: rows[i].ExpiresAt, CreatedAt: rows[i].CreatedAt, ResolvedAt: rows[i].ResolvedAt,
		}
		if rows[i].Response != nil && rows[i].Response["updatedInput"] != nil {
			requests[i].UpdatedInput = updatedInput
		}
		if rows[i].TurnID != nil {
			requests[i].TurnID = rows[i].TurnID.String()
		}
		if rows[i].PromptRunID != nil {
			requests[i].PromptRunID = rows[i].PromptRunID.String()
		}
		if rows[i].ModelCallID != nil {
			requests[i].ModelCallID = rows[i].ModelCallID.String()
		}
	}
	return requests, nil
}
