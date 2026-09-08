package query

import (
	"context"
	"fmt"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

type Store interface {
	ListPromptRuns(context.Context, database.PromptRunFilter) ([]database.PromptRun, error)
	RequestStore
	OverviewProjectionStore
}

// ComposeOptions supplies source aggregates and surface-specific projection
// rules to ComposeSession.
type ComposeOptions struct {
	Stored                    *session.Session
	Transcript                *session.Session
	SuppressPromptRunMessages bool
}

// ComposeSession runs every source that holds facts about a session over one
// aggregate.
//
// This is the single assembler. There were two — one behind /api/v1/sessions and
// one behind /api/chat/sessions — built from different code and returning
// different field sets, so the same session read two ways disagreed: one
// reported three approvals where the other reported none. Callers still resolve
// `stored` and `transcript` themselves, because they find them differently, but
// the contributor set, the precedence and the reconciliation live here.
//
// Source failures are returned rather than raised: a transcript that will not
// parse must not hide the approval that is blocking the run.
func ComposeSession(
	ctx context.Context,
	db Store,
	overview database.SessionOverview,
	opts ComposeOptions,
) (load.Result, []error, error) {
	contributors := []load.Contributor{load.Overview(OverviewFacts(overview))}
	if opts.Transcript != nil {
		contributors = append(contributors, load.Transcript(opts.Transcript))
	}
	if opts.Stored != nil {
		contributors = append(contributors, load.Database(opts.Stored))
	}

	stopPromptRuns := rpchttp.Track(ctx, "prompt_runs")
	runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &overview.ID})
	stopPromptRuns()
	if err != nil {
		return load.Result{}, nil, fmt.Errorf("list prompt runs for Captain session %s: %w", overview.ID, err)
	}
	if len(runs) > 0 {
		facts := PromptRunFacts(runs[0])
		facts.SuppressMessages = opts.SuppressPromptRunMessages
		contributors = append(contributors, load.PromptRun(facts))
	}

	requests, requestErr := SessionRequests(ctx, db, overview.ID)
	if requestErr != nil {
		return load.Result{}, nil, fmt.Errorf("list tool approvals for Captain session %s: %w", overview.ID, requestErr)
	}
	contributors = append(contributors, load.Requests(requests))

	facts, factsErr := ProjectionFacts(ctx, db, overview)
	if factsErr != nil {
		return load.Result{}, nil, fmt.Errorf("project Captain session %s overview: %w", overview.ID, factsErr)
	}
	contributors = append(contributors, load.Projection(facts))

	result, failures := load.Load(ctx, contributors...)
	for _, failure := range failures {
		logger.Warnf("captain session %s: %v", overview.ID, failure)
	}
	return result, failures, nil
}
