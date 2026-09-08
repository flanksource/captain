package load

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
)

// OverviewFacts is what the stored session row says about itself: who the
// session is, what it ran on, and what state it is in. These are properties of
// the row rather than of any transcript, so the overview supplies them first and
// every later source fills only what it left empty.
//
// It carries no messages, turns, usage or approvals: counts on the row are
// summaries of rows another contributor owns, and projecting them here would
// let a summary masquerade as the transcript it summarises.
type OverviewFacts struct {
	ID                string
	ProviderSessionID string
	Revision          int64
	Source            string
	Project           string
	CWD               string
	Slug              string
	Title             string
	InitialPrompt     string
	Version           string
	Provider          string
	Model             string
	ModelMode         api.RuntimeMode
	ExecutionMode     api.RuntimeMode
	ReasoningEffort   string
	HistoryFile       string
	LifecycleStatus   string
	ActivityState     string
	HealthState       string
	StateReason       string
	StartedAt         *time.Time
	EndedAt           *time.Time
}

// Overview contributes the stored row's own facts.
func Overview(facts OverviewFacts) Contributor { return overviewContributor{facts: facts} }

type overviewContributor struct {
	facts OverviewFacts
}

func (c overviewContributor) Source() Source { return SourceOverview }

func (c overviewContributor) Contribute(_ context.Context, aggregate *session.Session, _ *Provenance) error {
	if c.facts == (OverviewFacts{}) {
		return ErrNoFacts
	}
	setString(&aggregate.ID, c.facts.ID)
	setString(&aggregate.ProviderSessionID, c.facts.ProviderSessionID)
	if aggregate.Revision == 0 {
		aggregate.Revision = c.facts.Revision
	}
	setString(&aggregate.Source, c.facts.Source)
	setString(&aggregate.Project, c.facts.Project)
	setString(&aggregate.CWD, c.facts.CWD)
	setString(&aggregate.Slug, c.facts.Slug)
	setString(&aggregate.Title, c.facts.Title)
	setString(&aggregate.InitialPrompt, c.facts.InitialPrompt)
	setString(&aggregate.Version, c.facts.Version)
	setString(&aggregate.Provider, c.facts.Provider)
	setString(&aggregate.Model, c.facts.Model)
	setString(&aggregate.ReasoningEffort, c.facts.ReasoningEffort)
	setString(&aggregate.HistoryFile, c.facts.HistoryFile)
	setString(&aggregate.LifecycleStatus, c.facts.LifecycleStatus)
	setString(&aggregate.ActivityState, c.facts.ActivityState)
	setString(&aggregate.HealthState, c.facts.HealthState)
	setString(&aggregate.StateReason, c.facts.StateReason)
	setMode(&aggregate.ModelMode, c.facts.ModelMode)
	setMode(&aggregate.ExecutionMode, c.facts.ExecutionMode)
	setTime(&aggregate.StartedAt, c.facts.StartedAt)
	setTime(&aggregate.EndedAt, c.facts.EndedAt)
	return nil
}
