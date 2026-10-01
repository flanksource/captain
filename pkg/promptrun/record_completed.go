package promptrun

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// Completed is a run that executed outside Run — a direct provider call, a
// chat turn, or one that failed before it could be dispatched — filed after the
// fact through the same admission, start, binding and finish Run uses.
type Completed struct {
	Resolved api.ResolvedSpec
	// Runtime is the runtime that executed the run and Model the model that
	// answered; both are empty for a run that never reached a provider.
	Runtime api.Runtime
	Model   string
	// ProviderSessionID is the provider's own session id, when it reported one.
	ProviderSessionID string
	Outcome           Outcome
	Iterations        []database.UpsertPromptRunIterationInput
	Notices           []api.Notice
}

// RecordCompleted files a completed run and returns its id. Every store error
// is returned; the run row, once admitted, stays even when a later write fails.
func RecordCompleted(ctx context.Context, rec *Recording, done Completed) (uuid.UUID, error) {
	if rec == nil {
		return uuid.Nil, errors.New("promptrun: RecordCompleted requires a recording")
	}
	run, err := Admit(ctx, Input{Resolved: done.Resolved, Record: rec})
	if err != nil {
		return uuid.Nil, err
	}
	r := newRecorder(ctx, rec, run)
	r.prepare(done.Runtime, api.Model{Name: done.Model}, done.Resolved.Spec, done.ProviderSessionID)
	spec := done.Resolved.Spec
	startErr := r.start(&spec)
	return run.ID, errors.Join(startErr, r.settle(done.Outcome, done.Iterations, done.Notices, nil))
}
