package promptrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
)

// recorder files one admitted run's lifecycle: start, transcript binding,
// progress, and finish. Every write runs on a context detached from the run's
// own, so a stopped run is still written down.
//
// Writes that happen mid-run (an event callback, a progress snapshot) cannot
// fail the step that triggered them; the first such error is kept and fails the
// run when it finishes, like any other store error.
type recorder struct {
	ctx         context.Context
	db          *database.DB
	runID       uuid.UUID
	sessionID   uuid.UUID
	driver      string
	runtime     api.Runtime
	model       api.Model
	verifyOnly  bool
	knownSessID string

	mu            sync.Mutex
	boundSession  string
	generateTurn  int
	firstWriteErr error
}

func newRecorder(ctx context.Context, rec *Recording, run *database.PromptRun) *recorder {
	return &recorder{
		ctx: context.WithoutCancel(ctx), db: rec.DB, runID: run.ID, sessionID: run.SessionID, driver: rec.Runtime.Driver,
	}
}

// prepare records what dispatch settled: the runtime that executes the run, the
// model it resolved to, and a provider session id known before the first turn
// (a pre-assigned id, or the session a resumed run continues).
func (r *recorder) prepare(runtime api.Runtime, model api.Model, spec api.Spec, providerSessionID string) {
	r.runtime, r.model, r.verifyOnly = runtime, model, spec.IsVerifyOnly()
	r.knownSessID = strings.TrimSpace(firstNonEmpty(providerSessionID, spec.SessionID))
}

// start marks the run running with the spec that actually runs — the one setup
// already transformed — and the runtime it resolved to, then binds a provider
// session known up front.
func (r *recorder) start(spec *api.Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rendered, err := SpecDocument(*spec)
	if err != nil {
		return err
	}
	run, err := r.updateRun(func(run *database.PromptRun) (database.UpdatePromptRunInput, bool) {
		state, phase := database.PromptRunStateRunning, r.activePhase()
		runtime := r.resolvedRuntime(run.Runtime, spec)
		return database.UpdatePromptRunInput{State: &state, Phase: &phase, Runtime: &runtime, RenderedSpec: &rendered}, !finishedRun(run.State)
	})
	if err != nil {
		return fmt.Errorf("promptrun: record start: %w", err)
	}
	if err := r.recordCWD(run.SessionID, spec.Cwd()); err != nil {
		return err
	}
	return r.bindTranscript(r.knownSessID)
}

func (r *recorder) activePhase() database.PromptRunPhase {
	if r.verifyOnly {
		return database.PromptRunPhaseVerify
	}
	return database.PromptRunPhaseGenerate
}

// resolvedRuntime layers what this run resolved over what the run already
// recorded, so a resumed run that cannot name a field keeps the original's.
func (r *recorder) resolvedRuntime(current database.PromptRunRuntime, spec *api.Spec) database.PromptRunRuntime {
	merged := current
	merged.Resolved = database.PromptRunRuntimeSelection{
		Provider: firstNonEmpty(r.runtime.Provider, current.Resolved.Provider),
		Mode:     firstNonEmpty(string(r.runtime.Mode), current.Resolved.Mode),
		Model:    firstNonEmpty(r.model.Name, spec.Name, current.Resolved.Model),
		Effort:   firstNonEmpty(string(r.model.Effort), string(spec.Effort), current.Resolved.Effort),
	}
	return merged
}

// recordCWD stamps the directory the agent works in on the admission session: a
// per-run worktree is known only once setup has run.
func (r *recorder) recordCWD(sessionID uuid.UUID, cwd string) error {
	cwd = strings.TrimRight(strings.TrimSpace(cwd), "/")
	if cwd == "" {
		return nil
	}
	admission, err := r.db.GetSession(r.ctx, sessionID)
	if err != nil {
		return fmt.Errorf("promptrun: load admission session: %w", err)
	}
	if admission.CWD == cwd {
		return nil
	}
	if _, err := r.db.UpdateSessionState(r.ctx, database.UpdateSessionStateInput{
		ID: admission.ID, ExpectedVersion: admission.StateVersion, CWD: &cwd,
	}); err != nil {
		return fmt.Errorf("promptrun: record admission session cwd: %w", err)
	}
	return nil
}

// bindTranscript binds the provider's session id to the admission session and,
// for a runtime that leaves a transcript, the transcript session the monitor
// ingests into — then arms the monitor on it. Binding happens once per id.
func (r *recorder) bindTranscript(providerSessionID string) error {
	providerSessionID = strings.TrimSpace(providerSessionID)
	if providerSessionID == "" || providerSessionID == r.boundSession {
		return nil
	}
	run, err := r.db.GetPromptRun(r.ctx, r.runID)
	if err != nil {
		return err
	}
	admission, err := r.bindProviderSession(run.SessionID, providerSessionID)
	if err != nil {
		return err
	}
	transcript, err := r.transcriptSession(run, admission)
	if err != nil {
		return err
	}
	r.boundSession = providerSessionID
	if transcript == nil {
		return nil
	}
	return registerTranscript(r.ctx, r.db, transcript)
}

func (r *recorder) bindProviderSession(sessionID uuid.UUID, providerSessionID string) (*database.Session, error) {
	admission, err := r.db.GetSession(r.ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("promptrun: load admission session: %w", err)
	}
	if admission.ProviderSessionID == providerSessionID {
		return admission, nil
	}
	admission, err = r.db.UpdateSessionState(r.ctx, database.UpdateSessionStateInput{
		ID: admission.ID, ExpectedVersion: admission.StateVersion, ProviderSessionID: &providerSessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("promptrun: bind provider session %s: %w", providerSessionID, err)
	}
	return admission, nil
}

// transcriptSession is the session the provider's on-disk log is ingested into.
// A run binds its execution session once; a resumed run re-arms the one it has.
// A runtime that writes no transcript (the API mode) has none.
func (r *recorder) transcriptSession(run *database.PromptRun, admission *database.Session) (*database.Session, error) {
	if run.ExecutionSessionID != nil {
		return r.db.GetSession(r.ctx, *run.ExecutionSessionID)
	}
	source := TranscriptSource(r.runtime)
	if source == "" {
		return nil, nil
	}
	transcript := admission
	if admission.Source != source {
		var err error
		if transcript, err = r.createTranscriptSession(admission, source); err != nil {
			return nil, err
		}
	}
	if _, err := r.updateRun(func(*database.PromptRun) (database.UpdatePromptRunInput, bool) {
		return database.UpdatePromptRunInput{ExecutionSessionID: &transcript.ID}, true
	}); err != nil {
		return nil, fmt.Errorf("promptrun: bind execution session: %w", err)
	}
	return transcript, nil
}

// createTranscriptSession is the transcript child of an admission session that
// is not itself a transcript (a host's operation session). A run admitted on
// the transcript row itself — a continuation of a provider session — is its own.
func (r *recorder) createTranscriptSession(admission *database.Session, source string) (*database.Session, error) {
	description := ""
	if admission.Description != "" {
		description = "Agent session for " + admission.Description
	}
	transcript, err := r.db.CreateOrGetSession(r.ctx, database.CreateSessionInput{
		ProviderSessionID: admission.ProviderSessionID, Source: source, Provider: r.runtime.Provider,
		HostID: admission.HostID, ParentSessionID: &admission.ID, ParentRelation: database.SessionParentRelationTranscript,
		Project: admission.Project, CWD: admission.CWD, Title: admission.Title, InitialPrompt: admission.InitialPrompt,
		AgentType: r.driver, Description: description,
	})
	if err != nil {
		return nil, fmt.Errorf("promptrun: resolve transcript session: %w", err)
	}
	return transcript, nil
}

// TranscriptSource is the transcript a runtime leaves behind: every local mode
// of a provider family whose agent writes a log writes that agent's, and the
// API mode writes none.
func TranscriptSource(runtime api.Runtime) string {
	if runtime.Mode.Kind() != "cli" {
		return ""
	}
	provider, ok := runtime.ModelProvider()
	if !ok || (provider != api.Anthropic && provider != api.OpenAI) {
		return ""
	}
	return provider.AgentName
}

// registerTranscript arms the monitor on the transcript by id. A log that has
// not been flushed yet is the expected state at run start; the recon still
// finds it later.
func registerTranscript(ctx context.Context, db *database.DB, transcript *database.Session) error {
	path, err := monitor.RegisterTranscriptSource(ctx, db, transcript.ID, transcript.ProviderSessionID, transcript.Source)
	switch {
	case errors.Is(err, monitor.ErrTranscriptNotFound):
		logger.Debugf("promptrun: session %s has no %s transcript yet: %v", transcript.ID, transcript.Source, err)
		return nil
	case err != nil:
		return fmt.Errorf("promptrun: register transcript for session %s: %w", transcript.ID, err)
	}
	logger.Debugf("promptrun: registered transcript %s for session %s", path, transcript.ID)
	return nil
}

// observe binds the provider session the moment the stream names it, and moves
// the run back to generating when a retried turn starts producing.
func (r *recorder) observe(iter int, ev ai.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev.Kind == ai.EventSystem && ev.SessionID != "" {
		r.keep(r.bindTranscript(ev.SessionID))
	}
	if iter <= r.generateTurn || !generating(ev.Kind) {
		return
	}
	r.generateTurn = iter
	phase := database.PromptRunPhaseGenerate
	_, err := r.updateRun(func(run *database.PromptRun) (database.UpdatePromptRunInput, bool) {
		return database.UpdatePromptRunInput{Phase: &phase}, !finishedRun(run.State)
	})
	r.keep(err)
}

func generating(kind ai.EventKind) bool {
	return kind == ai.EventText || kind == ai.EventThinking || kind == ai.EventToolUse
}

// progress files an in-flight verification snapshot on the iteration it judges.
func (r *recorder) progress(report api.VerifyReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if report.Iteration < 1 {
		r.keep(fmt.Errorf("promptrun: verification progress for run %s has no iteration", r.runID))
		return
	}
	phase := database.PromptRunPhaseVerify
	if _, err := r.updateRun(func(run *database.PromptRun) (database.UpdatePromptRunInput, bool) {
		return database.UpdatePromptRunInput{Phase: &phase}, !finishedRun(run.State)
	}); err != nil {
		r.keep(fmt.Errorf("promptrun: record verification phase: %w", err))
		return
	}
	_, err := r.db.UpsertPromptRunIteration(r.ctx, database.UpsertPromptRunIterationInput{
		PromptRunID: r.runID, Iteration: report.Iteration,
		State: database.PromptRunIterationStateRunning, VerificationResult: &report,
	})
	r.keep(err)
}

func (r *recorder) keep(err error) {
	if err != nil && r.firstWriteErr == nil {
		r.firstWriteErr = err
	}
}

func (r *recorder) updateRun(build func(*database.PromptRun) (database.UpdatePromptRunInput, bool)) (*database.PromptRun, error) {
	return updateRun(r.ctx, r.db, r.runID, build)
}

// updateRun applies one change to the run's current version. build sees the
// run as stored and returns false to leave it alone. Other writers move the
// run's version too — the approval broker holds and releases it — so a lost
// optimistic race is re-read and rebuilt a bounded number of times.
func updateRun(ctx context.Context, db *database.DB, runID uuid.UUID, build func(*database.PromptRun) (database.UpdatePromptRunInput, bool)) (*database.PromptRun, error) {
	var err error
	for attempt := 0; attempt < versionRaceAttempts; attempt++ {
		var run *database.PromptRun
		if run, err = db.GetPromptRun(ctx, runID); err != nil {
			return nil, err
		}
		update, apply := build(run)
		if !apply {
			return run, nil
		}
		update.ID, update.ExpectedVersion = run.ID, run.Version
		updated, updateErr := db.UpdatePromptRun(ctx, update)
		if !errors.Is(updateErr, database.ErrPromptRunConflict) {
			return updated, updateErr
		}
		err = updateErr
	}
	return nil, err
}

// versionRaceAttempts bounds updateRun's re-reads: enough to outlast a broker's
// hold/release pair, few enough that a genuine conflict surfaces at once.
const versionRaceAttempts = 3

// hook is the recorder's place in the hook list: last, so its PreRun sees the
// request setup already transformed.
func (r *recorder) hook() *recordHook { return &recordHook{recorder: r} }

type recordHook struct{ recorder *recorder }

func (h *recordHook) Name() string { return "promptrun-recorder" }

func (h *recordHook) PreRun(hc *agent.HookContext) error { return h.recorder.start(hc.Request) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
