package load

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/session"
)

// aggregateContributor merges a session that some other layer already built —
// a parsed transcript, or the canonical rows read back from the store — into
// the composition. Transcript and Database differ only in precedence, so they
// share one merge: whichever runs first takes the contested facets and the
// other adds nothing to them.
type aggregateContributor struct {
	source Source
	from   *session.Session
}

func (c aggregateContributor) Source() Source { return c.source }

func (c aggregateContributor) Contribute(_ context.Context, aggregate *session.Session, prov *Provenance) error {
	if c.from == nil {
		return ErrNoFacts
	}
	mergeIdentity(aggregate, c.from)
	if prov.Claim(FacetMessages, c.source) {
		aggregate.Messages = append(aggregate.Messages, c.from.Messages...)
		aggregate.Events = append(aggregate.Events, c.from.Events...)
		aggregate.Capabilities = c.from.Capabilities
		aggregate.Root, aggregate.Agents = c.from.Root, c.from.Agents
		aggregate.Window = c.from.Window
	}
	if prov.Claim(FacetTurns, c.source) {
		aggregate.Turns = append(aggregate.Turns, c.from.Turns...)
	}
	if prov.Claim(FacetUsage, c.source) {
		aggregate.Usage, aggregate.Cost, aggregate.ToolCosts = c.from.Usage, c.from.Cost, c.from.ToolCosts
		if c.from.Context != nil {
			aggregate.Context = c.from.Context
		}
		if c.from.Budget != nil {
			aggregate.Budget = c.from.Budget
		}
	}
	// Requests and Approvals are deliberately not merged. The stored transcript
	// copy counts every operational tool use as an approval and reads as "200
	// approved"; captain_turn_requests is the only source allowed to claim
	// FacetApprovals, so the count is the same whichever branch built the rest.
	return nil
}

// mergeIdentity fills the descriptive fields the aggregate does not already
// carry. Everything here is first-write-wins, so a higher-precedence source
// that already resolved a field keeps it.
func mergeIdentity(aggregate, from *session.Session) {
	setString(&aggregate.ID, from.ID)
	setString(&aggregate.ProviderSessionID, from.ProviderSessionID)
	if aggregate.Revision == 0 {
		aggregate.Revision = from.Revision
	}
	setString(&aggregate.LifecycleStatus, from.LifecycleStatus)
	setString(&aggregate.ActivityState, from.ActivityState)
	setString(&aggregate.HealthState, from.HealthState)
	setString(&aggregate.StateReason, from.StateReason)
	setString(&aggregate.Source, from.Source)
	setString(&aggregate.Project, from.Project)
	setString(&aggregate.CWD, from.CWD)
	setString(&aggregate.Slug, from.Slug)
	setString(&aggregate.Title, from.Title)
	setString(&aggregate.InitialPrompt, from.InitialPrompt)
	setString(&aggregate.Version, from.Version)
	setString(&aggregate.Provider, from.Provider)
	setString(&aggregate.Model, from.Model)
	setString(&aggregate.ReasoningEffort, from.ReasoningEffort)
	setString(&aggregate.HistoryFile, from.HistoryFile)
	setString(&aggregate.ForkedFrom, from.ForkedFrom)
	setMode(&aggregate.ModelMode, from.ModelMode)
	setMode(&aggregate.ExecutionMode, from.ExecutionMode)
	setTime(&aggregate.StartedAt, from.StartedAt)
	setTime(&aggregate.EndedAt, from.EndedAt)
	if aggregate.Runtime == nil {
		aggregate.Runtime = from.Runtime
	}
	if aggregate.Git == (session.GitState{}) {
		aggregate.Git = from.Git
	}
	if len(aggregate.Files.Read) == 0 && len(aggregate.Files.Written) == 0 {
		aggregate.Files = from.Files
	}
	if len(aggregate.Todos) == 0 {
		aggregate.Todos = from.Todos
	}
	if aggregate.Plan == nil {
		aggregate.Plan = from.Plan
	}
	if len(aggregate.Health) == 0 {
		aggregate.Health = from.Health
	}
	if aggregate.Live == nil {
		aggregate.Live = from.Live
	}
}

func setString(target *string, value string) {
	if *target == "" {
		*target = value
	}
}

func setMode[T ~string](target *T, value T) {
	if *target == "" {
		*target = value
	}
}

func setTime(target **time.Time, value *time.Time) {
	if *target == nil && value != nil && !value.IsZero() {
		*target = value
	}
}
