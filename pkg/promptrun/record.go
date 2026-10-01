package promptrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/captain/pkg/sessiontree"
	"github.com/google/uuid"
)

// Recording files a run in Captain's store. Captain owns every write to its
// tables: a host describes where the run belongs and writes only its own rows,
// through Link, inside the admission transaction.
//
// The run is admitted (pending, with its plain spec and resolution metadata)
// before anything is dispatched, started once setup has run, and finished with
// its outcome, iterations and notices. A store write that fails fails the run.
type Recording struct {
	// DB is the Captain store the run is filed in.
	DB *database.DB
	// Placement names the session the run is admitted on.
	Placement Placement
	// PromptRunID is the run's id when the host chose it; nil generates one.
	// With AdmissionKey it makes admission idempotent: a replay resolves the
	// run already admitted instead of creating a second one.
	PromptRunID  uuid.UUID
	AdmissionKey string
	Origin       string
	SpecProfile  string
	BatchID      *uuid.UUID
	// InputPlanID / InputPlanRevisionID name the plan the run executes.
	InputPlanID         *uuid.UUID
	InputPlanRevisionID *uuid.UUID
	// PromptMarkdown and VerificationMarkdown are the human-readable prompt and
	// definition of done the run was given, stored as written.
	PromptMarkdown       string
	VerificationMarkdown string
	// Runtime is what the host asked for: its run mode, driver and requested
	// selection. The resolved selection is filled in when the run starts.
	Runtime database.PromptRunRuntime
	// Link writes the host's own rows for the admitted run. It runs inside the
	// admission transaction; an error rolls back the sessions and the run, and
	// nothing is dispatched.
	Link func(ctx context.Context, tx *database.DB, run *database.PromptRun) error
	// Outcome classifies the finished run; nil means DefaultOutcome. A host
	// whose prompt answers in an envelope of its own (an "ask" status in its
	// structured output) maps that here.
	Outcome func(result Result, runErr error, stopped bool) (Outcome, error)
}

// Placement is the session a run is admitted on: an existing one, or a session
// tree to ensure in the admission transaction, whose last entry is the
// admission session.
type Placement struct {
	SessionID uuid.UUID
	Sessions  []database.CreateSessionInput
}

func (p Placement) validate() error {
	switch {
	case p.SessionID != uuid.Nil && len(p.Sessions) > 0:
		return errors.New("promptrun: recording placement names both an existing session and a session tree")
	case p.SessionID == uuid.Nil && len(p.Sessions) == 0:
		return errors.New("promptrun: recording placement names no session: set Placement.SessionID or Placement.Sessions")
	}
	return nil
}

func (p Placement) resolve(ctx context.Context, tx *database.DB) (*database.Session, error) {
	if p.SessionID != uuid.Nil {
		return tx.GetSession(ctx, p.SessionID)
	}
	sessions, err := sessiontree.EnsureTx(ctx, tx, p.Sessions)
	if err != nil {
		return nil, err
	}
	return sessions[len(sessions)-1], nil
}

// Admit files in.Record's admission and dispatches nothing: the placement
// sessions, the pending run with in.Resolved's plain spec and its resolution
// metadata (in.RuntimePresets, in.RuntimeProfile), then the host's Link — all
// in one transaction, which a Link error rolls back. A replayed AdmissionKey
// resolves the run already admitted; one that already finished is
// ErrRunFinished. Run admits through it before dispatch.
func Admit(ctx context.Context, in Input) (*database.PromptRun, error) {
	rec := in.Record
	if rec == nil {
		return nil, errors.New("promptrun: admitting a run requires a recording")
	}
	if rec.DB == nil {
		return nil, errors.New("promptrun: recording has no database")
	}
	if err := rec.Placement.validate(); err != nil {
		return nil, err
	}
	rendered, err := SpecDocument(in.Resolved.Spec)
	if err != nil {
		return nil, err
	}
	metadata, err := RunMetadata(in.Resolved, in.RuntimePresets, in.RuntimeProfile)
	if err != nil {
		return nil, err
	}
	var out *database.PromptRun
	err = rec.DB.Transaction(ctx, func(tx *database.DB) error {
		session, err := rec.Placement.resolve(ctx, tx)
		if err != nil {
			return fmt.Errorf("promptrun: place run: %w", err)
		}
		run, err := tx.CreatePromptRun(ctx, database.CreatePromptRunInput{
			ID: rec.PromptRunID, SessionID: session.ID, BatchID: rec.BatchID,
			InputPlanID: rec.InputPlanID, InputPlanRevisionID: rec.InputPlanRevisionID,
			Origin: rec.Origin, SpecProfile: rec.SpecProfile, AdmissionKey: rec.AdmissionKey,
			RenderedSpec: rendered, Metadata: metadata, Runtime: rec.Runtime,
			PromptMarkdown: rec.PromptMarkdown, VerificationMarkdown: rec.VerificationMarkdown,
		})
		if err != nil {
			return fmt.Errorf("promptrun: admit run: %w", err)
		}
		if finishedRun(run.State) {
			return fmt.Errorf("%w: admission resolved run %s, which is %s", ErrRunFinished, run.ID, run.State)
		}
		if rec.Link != nil {
			if err := rec.Link(ctx, tx, run); err != nil {
				return fmt.Errorf("promptrun: link run %s: %w", run.ID, err)
			}
		}
		out = run
		return nil
	})
	return out, err
}

// SpecDocument is the spec as the plain JSON object rendered_spec stores. Every
// writer of captain_prompt_runs.rendered_spec builds it here.
func SpecDocument(spec api.Spec) (map[string]any, error) {
	return jsonObject("rendered spec", spec)
}

// RunMetadata is how the spec came to be: the layer trace, per-field
// provenance, resolution warnings and the runtime catalog selections, as
// captain_prompt_runs.metadata stores it. Empty entries are omitted rather
// than stored as empty values; nil presets or profile record none.
func RunMetadata(resolved api.ResolvedSpec, presets *runtimeprofiles.PresetResolution, profile *runtimeprofiles.Resolution) (map[string]any, error) {
	values := map[string]any{}
	if len(resolved.Trace) > 0 {
		values["specTrace"] = resolved.Trace
	}
	if len(resolved.Provenance) > 0 {
		values["specProvenance"] = resolved.Provenance
	}
	if len(resolved.Warnings) > 0 {
		values["specWarnings"] = resolved.Warnings
	}
	if presets != nil && len(presets.Presets) > 0 {
		values["runtimePresets"] = presets.Presets
	}
	if profile != nil {
		values["runtimeProfile"] = struct {
			Profile runtimeprofiles.Profile  `json:"profile"`
			Presets []runtimeprofiles.Preset `json:"presets"`
		}{Profile: profile.Profile, Presets: profile.Presets}
	}
	return jsonObject("run metadata", values)
}

func jsonObject(what string, value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("promptrun: encode %s: %w", what, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("promptrun: decode %s as an object: %w", what, err)
	}
	return out, nil
}

func finishedRun(state database.PromptRunState) bool {
	switch state {
	case database.PromptRunStateSucceeded, database.PromptRunStateFailed, database.PromptRunStateCancelled:
		return true
	}
	return false
}
