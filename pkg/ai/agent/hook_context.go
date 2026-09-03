package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// HookContext carries what hooks read/mutate: the request for this iteration and
// the accumulating response, whose Workspace holds the run's working-dir state.
type HookContext struct {
	context.Context
	// Request is the CURRENT request: PreRun hooks rewrite it (setup replaces the
	// checkout it performed with where that landed), and the runner replaces it
	// wholesale with each verify-driven retry.
	Request *ai.Request
	// Original is the request the run started from, before any hook rewrote it.
	// It is the only place a Post hook can read what the run was *asked* to do —
	// which repo to check out, which branch to isolate on — while Request says
	// where that ended up. Cloned once by Run, so mutating it affects nothing.
	Original ai.Request
	Response *ai.Response
	// Iteration is the loop's 0-BASED index of the turn currently in scope: the
	// turn about to run, or — while verify hooks vote and PhaseTurn dispatches —
	// the one that just completed. A hook reporting the turn to a person or to
	// the iteration store adds one (see VerifyResult.Iteration), which is 1-based.
	//
	// It only ever names a turn that actually executes; the loop's final
	// BuildRequest call, the one that ends the run, does not advance it.
	Iteration int
	Scope     Scope

	// Hooks is the run's hook list, so a hook can detect an incompatible peer
	// rather than silently competing with it (see EnsureSingleIsolator).
	Hooks []any

	// Phase is the boundary currently being dispatched; it is only meaningful
	// inside a Post hook, which also receives it as an argument.
	Phase Phase
	// Turn is the loop iteration that just completed. Set only during PhaseTurn;
	// nil everywhere else.
	Turn *ai.LoopIteration

	// Verified and Failed describe the run's outcome so PhaseAgent/PhaseRun
	// hooks (e.g. the worktree merge/cleanup gate) can act on it. Runner.Run
	// sets both right after the loop ends; they are meaningless beforehand, and
	// in particular are not yet settled during PhaseTurn.
	//
	// Verified mirrors VerifyPassed(result.Verdicts) — true when the last verify
	// verdict passed, or trivially true when no Verify hooks ran at all.
	Verified bool
	// Failed is true when the generate/verify run itself returned an error
	// (a provider failure, not a failing verdict).
	Failed bool

	// emit publishes an event on the run's stream, so what a hook does between
	// turns reaches the same renderers as what the model does during them. Set by
	// Runner.Run; nil in a hand-built context, which Notify tolerates.
	emit func(ai.Event)
}

// Notify reports one thing this hook did, in the run's own voice. It reaches the
// live stream as an ai.EventSystem and is buffered on the workspace with its
// timestamp, so a caller can persist it into the transcript once the run's
// session id is known — a hook firing mid-turn cannot know it yet.
//
// Purely informational: a hook that failed returns an error, it does not Notify.
func (hc *HookContext) Notify(format string, args ...any) {
	hc.NotifyEvent(ai.Event{Kind: ai.EventSystem, Text: fmt.Sprintf(format, args...)})
}

// NotifyEvent is Notify for a hook whose report has an event kind of its own and
// structured fields to go with it — a verify verdict is not a generic system
// line, and a renderer that must colour a pass and a failure differently, or a
// dashboard that filters on them, cannot recover that from the text.
//
// ev.Text is the human-readable report and is what the notice records; a typed
// verify report on ev.Raw rides along on the notice so a stored transcript
// carries the tree the live stream drew rather than only its headline. The
// remaining fields travel on the live stream only. An event with no text records
// nothing: a notice exists to be read.
func (hc *HookContext) NotifyEvent(ev ai.Event) {
	if ev.Text == "" {
		return
	}
	if ev.Kind == "" {
		ev.Kind = ai.EventSystem
	}
	notice := api.Notice{At: time.Now(), Phase: string(hc.Phase), Text: ev.Text, Kind: ev.Kind}
	if report, ok := ev.Raw.(*api.VerifyReport); ok {
		notice.Report = report
	}
	hc.Workspace().AddNoticeRecord(notice)
	hc.Emit(ev)
}

// Emit publishes one event on the run's live stream and records nothing. It is
// for the reports that are true only while they are being read — a verifier's
// in-flight progress, redrawn in place by whatever is watching — as opposed to
// the ones a reader should still find in the transcript afterwards.
//
// Routing progress through NotifyEvent wrote every coalesced snapshot into the
// workspace's notices and from there into the persisted transcript, so a long
// check buried its own verdict under a stack of superseded counts. Such an event
// needs no Text: it carries the structure a renderer draws, not prose about it.
//
// nil-safe: a hand-built HookContext has no stream, and emitting into it is a
// no-op rather than a panic.
func (hc *HookContext) Emit(ev ai.Event) {
	if ev.Kind == "" {
		ev.Kind = ai.EventSystem
	}
	if hc.emit != nil {
		hc.emit(ev)
	}
}

// Workspace returns the run's working-dir state, allocating it if needed (so it
// is never nil for a hook to read/mutate).
func (hc *HookContext) Workspace() *api.Workspace {
	if hc.Response.Workspace == nil {
		hc.Response.Workspace = &api.Workspace{}
	}
	return hc.Response.Workspace
}
