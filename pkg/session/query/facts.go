package query

import (
	"context"

	"github.com/google/uuid"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

// ProjectionFacts gathers the stored projection of a session for composition.
//
// It is the adapter between the store's rows and load's fact DTOs: load never
// imports pkg/database, so the row reading lives here and the merge rules live
// there. Both the v1 session route and the chat route build their aggregate from
// this one gatherer, which is what stops the two drifting apart again.
func ProjectionFacts(
	ctx context.Context,
	db OverviewProjectionStore,
	overview database.SessionOverview,
) (load.ProjectionFacts, error) {
	facts := load.ProjectionFacts{
		Metadata: session.DecodeMetadata(overview.Metadata),
		Git:      session.DecodeGitState(overview.Git),
		Context:  overviewContext(overview),
	}

	plans, err := db.ListPlans(ctx, database.PlanFilter{SourceSessionID: &overview.ID})
	if err != nil {
		return load.ProjectionFacts{}, err
	}
	facts.Plan = planFromNative(plans)

	rootID := overview.ID
	if overview.RootSessionID != nil {
		rootID = *overview.RootSessionID
	}
	thread, err := db.ListThreadSessionOverviews(ctx, rootID)
	if err != nil {
		return load.ProjectionFacts{}, err
	}
	facts.ThreadFiles = descendantFiles(overview.ID, thread)
	return facts, nil
}

// RequestStore reads the durable tool-approval requests of a session.
type RequestStore interface {
	ListTurnRequests(context.Context, database.TurnRequestFilter) ([]database.TurnRequest, error)
}

type OverviewProjectionStore interface {
	ListThreadSessionOverviews(context.Context, uuid.UUID) ([]database.SessionOverview, error)
	ListPlans(context.Context, database.PlanFilter) ([]database.Plan, error)
}

// SessionRequests reads a session's tool approval requests for composition.
//
// Every route loads these, unconditionally. They used to be read in one of three
// branches, so a run suspended on an approval reported none whenever either of
// the other two served the request.
func SessionRequests(ctx context.Context, db RequestStore, sessionID uuid.UUID) ([]session.Request, error) {
	rows, err := db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	return projectSessionRequests(rows)
}

// OverviewFacts adapts the stored session row for composition.
func OverviewFacts(overview database.SessionOverview) load.OverviewFacts {
	return load.OverviewFacts{
		ID:                overview.ID.String(),
		ProviderSessionID: stringValue(overview.ProviderSessionID),
		Revision:          overview.StateVersion,
		Source:            overview.Source,
		Project:           stringValue(overview.Project),
		CWD:               stringValue(overview.CWD),
		Slug:              stringValue(overview.Slug),
		Title:             stringValue(overview.Title),
		InitialPrompt:     stringValue(overview.InitialPrompt),
		Version:           stringValue(overview.CLIVersion),
		Provider:          overview.Provider,
		Model:             stringValue(overview.Model),
		ModelMode:         api.RuntimeMode(stringValue(overview.ModelMode)),
		ReasoningEffort:   stringValue(overview.Effort),
		HistoryFile:       stringOr(overview.HistoryFile, stringValue(overview.Path)),
		LifecycleStatus:   overview.LifecycleStatus,
		ActivityState:     overview.ActivityState,
		HealthState:       overview.HealthState,
		StateReason:       stringValue(overview.StateReason),
		StartedAt:         overview.StartedAt,
		EndedAt:           overview.EndedAt,
	}
}

// PromptRunFacts adapts a prompt run for composition.
//
// The runtime records both what was asked for and what the registry resolved.
// Composition is given one value per field — resolved first, requested as the
// fallback, mixed per field rather than choosing a selection wholesale — so that
// precedence is decided here once instead of inside every consumer.
func PromptRunFacts(run database.PromptRun) load.PromptRunFacts {
	resolved, requested := run.Runtime.Resolved, run.Runtime.Requested
	return load.PromptRunFacts{
		RunID:          run.ID.String(),
		State:          string(run.State),
		PromptMarkdown: run.PromptMarkdown,
		ResultText:     run.ResultText,
		ResultJSON:     run.ResultJSON,
		RenderedSpec:   run.RenderedSpec,
		Error:          run.Error,
		Provider:       firstNonEmpty(resolved.Provider, requested.Provider),
		Model:          firstNonEmpty(resolved.Model, requested.Model),
		Mode:           firstNonEmpty(resolved.Mode, requested.Mode),
		Effort:         firstNonEmpty(resolved.Effort, requested.Effort),
		QueuedAt:       run.QueuedAt,
		StartedAt:      run.StartedAt,
		FinishedAt:     run.FinishedAt,
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringOr(value *string, fallback string) string {
	if candidate := stringValue(value); candidate != "" {
		return candidate
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
