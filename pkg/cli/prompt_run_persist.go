package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// promptRunRecordInput is a completed captain-launched prompt run to persist.
type promptRunRecordInput struct {
	Rendered   PromptRenderResult
	RunID      string
	Binding    *promptSessionBinding
	SessionID  string
	Model      string
	Provider   *api.ModelProvider
	Mode       api.RuntimeMode
	BatchID    *uuid.UUID
	ResultText string
	ResultJSON map[string]any
	Error      string
	// State overrides the state the run row is recorded under. Empty derives it
	// from Error, which is what a run that reached its own end needs. An
	// interrupted run is neither succeeded nor failed — its work was cut off, not
	// judged — so the run path stamps `cancelled` explicitly.
	State database.PromptRunState
	// Iterations is one row per executed loop turn (1-based), built by
	// promptRunIterationRecords from the runner's own loop and verdicts.
	Iterations []database.UpsertPromptRunIterationInput
}

// persistPromptRun records a captain-launched run against its session in the
// native store and registers the transcript for live tailing. Persistence
// failures are reported loudly but never fail the completed run itself.
func persistPromptRun(ctx context.Context, input promptRunRecordInput) {
	if input.Binding == nil && strings.TrimSpace(input.SessionID) == "" {
		return
	}
	source := transcriptSource(input.Provider, input.Mode)
	if source == "" {
		source = "claude"
	}
	db, err := captainDefaultDB(ctx)
	if err != nil {
		log.Errorf("persist prompt run for session %s: %v", input.SessionID, err)
		return
	}
	var session *database.Session
	if input.Binding != nil {
		session, err = db.GetSession(ctx, input.Binding.SessionID)
		if err == nil && strings.TrimSpace(input.SessionID) != "" {
			providerSessionID := strings.TrimSpace(input.SessionID)
			session, err = db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
				ID: session.ID, ExpectedVersion: session.StateVersion, ProviderSessionID: &providerSessionID,
			})
		}
	} else {
		session, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: input.SessionID, Source: source, HostID: captainHostID(),
			Provider: providerName(input.Provider), CWD: input.Rendered.Input.Cwd(),
		})
	}
	if err != nil {
		log.Errorf("persist prompt run for session %s: %v", firstNonEmpty(input.SessionID, bindingSessionID(input.Binding)), err)
		return
	}
	batchID := input.BatchID
	if input.Binding != nil {
		batchID = &input.Binding.BatchID
	}
	var runID uuid.UUID
	err = db.Transaction(ctx, func(tx *database.DB) error {
		run, createErr := tx.CreatePromptRun(ctx, database.CreatePromptRunInput{
			SessionID:    session.ID,
			BatchID:      batchID,
			Origin:       "captain",
			AdmissionKey: input.RunID,
			RenderedSpec: renderedSpecMap(input.Rendered),
			Runtime: database.PromptRunRuntime{
				Mode: "run",
				Resolved: database.PromptRunRuntimeSelection{
					Provider: providerName(input.Provider), Mode: string(input.Mode),
					Model: input.Model, Effort: string(input.Rendered.Config.Model.Effort),
				},
			},
			PromptMarkdown: input.Rendered.Input.Prompt.User,
		})
		if createErr != nil {
			return createErr
		}
		finished := database.PromptRunPhaseFinished
		state := input.State
		if state == "" {
			state = database.PromptRunStateSucceeded
			if input.Error != "" {
				state = database.PromptRunStateFailed
			}
		}
		update := database.UpdatePromptRunInput{
			ID: run.ID, ExpectedVersion: run.Version, Phase: &finished, State: &state,
		}
		if input.ResultText != "" {
			update.ResultText = &input.ResultText
		}
		if input.ResultJSON != nil {
			update.ResultJSON = &input.ResultJSON
		}
		if input.Error != "" {
			update.Error = &input.Error
		}
		if _, updateErr := tx.UpdatePromptRun(ctx, update); updateErr != nil {
			return updateErr
		}
		runID = run.ID
		return nil
	})
	if err != nil {
		log.Errorf("persist prompt run for session %s: %v", firstNonEmpty(input.SessionID, bindingSessionID(input.Binding)), err)
		return
	}
	if err := upsertPromptRunIterations(ctx, db, runID, input.Iterations); err != nil {
		log.Errorf("persist prompt run %s iterations for session %s: %v", input.RunID, firstNonEmpty(input.SessionID, bindingSessionID(input.Binding)), err)
	}
	lifecycle := database.SessionLifecycleSucceeded
	if input.Error != "" || input.State == database.PromptRunStateCancelled {
		lifecycle = database.SessionLifecycleFailed
	}
	updatePromptSessionLifecycle(ctx, session.ID, lifecycle, input.Error)
	trackLaunchedTranscript(input, source)
}

// upsertPromptRunIterations writes the run's per-turn rows after the run row
// has been committed, one row at a time and all of them: a turn the store
// refuses (a report that fails its own validation) must cost exactly that row.
// Writing them inside the run's transaction took the run itself down with a bad
// turn, leaving no record that the run had happened at all; and stopping at the
// first refusal hid the turns after it. Every refusal is reported.
func upsertPromptRunIterations(ctx context.Context, db *database.DB, runID uuid.UUID, records []database.UpsertPromptRunIterationInput) error {
	var errs []error
	for _, record := range records {
		record.PromptRunID = runID
		if _, err := db.UpsertPromptRunIteration(ctx, record); err != nil {
			errs = append(errs, fmt.Errorf("iteration %d: %w", record.Iteration, err))
		}
	}
	return errors.Join(errs...)
}

func bindingSessionID(binding *promptSessionBinding) string {
	if binding == nil {
		return ""
	}
	return binding.SessionID.String()
}

// trackLaunchedTranscript arms the serve monitor's fsnotify tail on the
// freshly launched session's transcript so it ingests immediately instead of
// waiting for the next process poll or backfill.
func trackLaunchedTranscript(input promptRunRecordInput, source string) {
	mon := serveMonitor()
	if mon == nil {
		return
	}
	path := historyFileForRun(input.Provider, input.Mode, input.SessionID, input.Rendered.Input.Cwd())
	if path != "" {
		mon.TrackTranscript(path, source)
	}
}

// transcriptSource names the transcript a runtime leaves behind. It is a
// property of the provider family plus running locally at all: every local
// Claude mode writes a `claude` transcript, and the API mode writes none.
func transcriptSource(provider *api.ModelProvider, mode api.RuntimeMode) string {
	if provider == nil || mode.Kind() != "cli" {
		return ""
	}
	switch provider {
	case api.Anthropic, api.OpenAI:
		return provider.AgentName
	default:
		return ""
	}
}

func providerName(p *api.ModelProvider) string {
	if p == nil {
		return ""
	}
	return p.Name
}

// renderedSpecMap round-trips the realized prompt render into the jsonb shape
// stored on the prompt run.
func renderedSpecMap(rendered PromptRenderResult) map[string]any {
	raw, err := json.Marshal(rendered)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}
