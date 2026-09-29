package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/google/uuid"
)

// promptRecordingInput names where a captain-launched run is filed.
type promptRecordingInput struct {
	Rendered PromptRenderResult
	// RunID is the run's admission key: the stream id the run is watched under.
	RunID   string
	Binding *promptSessionBinding
	// SessionID is the captain session a run with neither a batch binding nor
	// a provider session to continue is admitted on — a chat's, shared by all of
	// its turns. Nil gives the run a session of its own.
	SessionID uuid.UUID
}

// promptRecording is how `captain prompt run` hands its run to promptrun to
// file: captain owns the placement, promptrun every write. It is nil when the
// session store cannot be opened (see unrecordedWithoutStore); once the store
// is open, every placement and write error is the run's.
func promptRecording(ctx context.Context, input promptRecordingInput) (*promptrun.Recording, error) {
	db := unrecordedWithoutStore(ctx, input.RunID)
	if db == nil {
		return nil, nil
	}
	placement, err := promptPlacement(ctx, db, input)
	if err != nil {
		return nil, fmt.Errorf("record prompt run: %w", err)
	}
	rendered := input.Rendered
	rec := &promptrun.Recording{
		DB: db, Placement: placement, Origin: "captain", AdmissionKey: input.RunID,
		PromptMarkdown: rendered.Input.Prompt.User,
		Runtime: database.PromptRunRuntime{Mode: "run", Requested: database.PromptRunRuntimeSelection{
			Provider: rendered.Provider, Mode: rendered.Mode, Model: rendered.Model, Effort: string(rendered.Config.Model.Effort),
		}},
	}
	if input.Binding != nil {
		rec.BatchID = &input.Binding.BatchID
	}
	return rec, nil
}

// unrecordedWithoutStore opens the session store a CLI run is filed in, or
// returns nil when there is none to open — CAPTAIN_SESSION_DB_URL=off, or no
// embedded postgres on this machine. The CLI runs prompts without a store, as
// it always has: the failure is logged as an error naming the run, and the run
// goes unrecorded. It is the only store failure that does not fail the run.
func unrecordedWithoutStore(ctx context.Context, runID string) *database.DB {
	db, err := captainDefaultDB(ctx)
	if err != nil {
		log.Errorf("prompt run %q will not be recorded: %v", runID, err)
		return nil
	}
	return db
}

// promptPlacement is the session a run is admitted on: a batch member's own
// session; the chat's session; the transcript session of the provider session
// a run continues, which is where a host that parked a run looks for the
// continuation; or a fresh captain session.
func promptPlacement(ctx context.Context, db *database.DB, input promptRecordingInput) (promptrun.Placement, error) {
	rendered := input.Rendered
	switch {
	case input.Binding != nil:
		return promptrun.Placement{SessionID: input.Binding.SessionID}, nil
	case input.SessionID != uuid.Nil:
		return promptrun.Placement{Sessions: []database.CreateSessionInput{captainPromptSession(input.SessionID, rendered)}}, nil
	}
	resumed := strings.TrimSpace(rendered.Input.SessionID)
	source := promptrun.TranscriptSource(api.Runtime{Provider: rendered.Provider, Mode: api.RuntimeMode(rendered.Mode)})
	if resumed == "" || source == "" {
		return promptrun.Placement{Sessions: []database.CreateSessionInput{captainPromptSession(uuid.New(), rendered)}}, nil
	}
	session, err := db.GetSessionByIdentity(ctx, resumed, source, "", "")
	if err == nil {
		return promptrun.Placement{SessionID: session.ID}, nil
	}
	if !errors.Is(err, database.ErrSessionNotFound) {
		return promptrun.Placement{}, err
	}
	return promptrun.Placement{Sessions: []database.CreateSessionInput{{
		ProviderSessionID: resumed, Source: source, Provider: rendered.Provider,
		HostID: captainHostID(), CWD: rendered.Input.Cwd(),
	}}}, nil
}

func captainPromptSession(id uuid.UUID, rendered PromptRenderResult) database.CreateSessionInput {
	return database.CreateSessionInput{
		ID: id, Source: "captain", Provider: rendered.Provider, HostID: captainHostID(),
		CWD: rendered.Input.Cwd(), Title: rendered.Name, InitialPrompt: rendered.Input.Prompt.User, AgentType: "prompt",
	}
}

// promptResolution is the render's resolution with the spec that actually
// runs: the request after attachments were resolved against the local store.
func promptResolution(rendered PromptRenderResult, spec api.Spec) api.ResolvedSpec {
	resolved := rendered.Resolution
	resolved.Spec = spec
	return resolved
}

// recordCompletedRun files a run that executed outside promptrun.Run — a
// direct provider call, a chat turn, a run that failed before dispatch.
func recordCompletedRun(ctx context.Context, input promptRecordingInput, done promptrun.Completed) error {
	rec, err := promptRecording(context.WithoutCancel(ctx), input)
	if err != nil {
		return err
	}
	return recordCompleted(ctx, rec, done)
}

// recordCompleted files a completed run through a recording already made; a
// nil recording is a run with no store to file it in.
func recordCompleted(ctx context.Context, rec *promptrun.Recording, done promptrun.Completed) error {
	if rec == nil {
		return nil
	}
	if _, err := promptrun.RecordCompleted(context.WithoutCancel(ctx), rec, done); err != nil {
		return fmt.Errorf("record prompt run %q: %w", rec.AdmissionKey, err)
	}
	return nil
}

// unstartedRun is a run that failed before it reached a provider.
func unstartedRun(rendered PromptRenderResult, err error) promptrun.Completed {
	return promptrun.Completed{
		Resolved: promptResolution(rendered, rendered.Input),
		Outcome:  promptrun.Outcome{State: database.PromptRunStateFailed, Error: err.Error()},
	}
}

func providerName(p *api.ModelProvider) string {
	if p == nil {
		return ""
	}
	return p.Name
}
