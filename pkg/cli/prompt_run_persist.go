package cli

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

// promptRunRecordInput is a completed captain-launched prompt run to persist.
type promptRunRecordInput struct {
	Rendered   PromptRenderResult
	RunID      string
	SessionID  string
	Model      string
	Backend    string
	ResultText string
	Error      string
}

// persistPromptRun records a captain-launched run against its session in the
// native store and registers the transcript for live tailing. Persistence
// failures are reported loudly but never fail the completed run itself.
func persistPromptRun(ctx context.Context, input promptRunRecordInput) {
	if strings.TrimSpace(input.SessionID) == "" {
		return
	}
	source := backendSource(api.Backend(input.Backend))
	if source == "" {
		source = "claude"
	}
	db, err := captainDB(ctx)
	if err != nil {
		log.Errorf("persist prompt run for session %s: %v", input.SessionID, err)
		return
	}
	session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
		ProviderSessionID: input.SessionID, Source: source, HostID: captainHostID(),
		CWD: input.Rendered.Input.Cwd(),
	})
	if err != nil {
		log.Errorf("persist prompt run for session %s: %v", input.SessionID, err)
		return
	}
	run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{
		SessionID:      session.ID,
		Origin:         "captain",
		AdmissionKey:   input.RunID,
		RenderedSpec:   renderedSpecMap(input.Rendered),
		PromptMarkdown: input.Rendered.Input.Prompt.User,
	})
	if err != nil {
		log.Errorf("persist prompt run for session %s: %v", input.SessionID, err)
		return
	}
	finished := database.PromptRunPhaseFinished
	state := database.PromptRunStateSucceeded
	if input.Error != "" {
		state = database.PromptRunStateFailed
	}
	update := database.UpdatePromptRunInput{
		ID: run.ID, ExpectedVersion: run.Version, Phase: &finished, State: &state,
	}
	if input.ResultText != "" {
		update.ResultText = &input.ResultText
	}
	if input.Error != "" {
		update.Error = &input.Error
	}
	if _, err := db.UpdatePromptRun(ctx, update); err != nil {
		log.Errorf("finalize prompt run %s: %v", run.ID, err)
	}
	trackLaunchedTranscript(input, source)
}

// trackLaunchedTranscript arms the serve monitor's fsnotify tail on the
// freshly launched session's transcript so it ingests immediately instead of
// waiting for the next process poll or backfill.
func trackLaunchedTranscript(input promptRunRecordInput, source string) {
	mon := serveMonitor()
	if mon == nil {
		return
	}
	path := historyFileForRun(api.Backend(input.Backend), input.SessionID, input.Rendered.Input.Cwd())
	if path != "" {
		mon.TrackTranscript(path, source)
	}
}

// backendSource maps a prompt backend to the transcript source it produces.
func backendSource(backend api.Backend) string {
	switch backend {
	case api.BackendClaudeAgent, api.BackendClaudeCLI, api.BackendClaudeCmux:
		return "claude"
	case api.BackendCodexAgent, api.BackendCodexCLI, api.BackendCodexCmux:
		return "codex"
	default:
		return ""
	}
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
