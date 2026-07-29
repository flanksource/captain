// Package setup materialises a run's Spec.Setup — dotenv files, connections, and
// the git checkout or worktree — and rewrites the spec to describe the result.
//
// The rewrite is the point. Setup is not configuration a provider reads; it is an
// action that changes the world, and a spec that still says "clone this URL"
// after the clone exists is a spec that clones twice when replayed. Apply
// consumes the request and replaces it with where it landed, so what remains is
// Cwd and Env — which is exactly the surface a provider is allowed to read
// (see api.Spec.Cwd and the CLI providers' commandEnv). A spec that has been
// through Apply is idempotent: applying it again performs no checkout.
//
// Two faces, one implementation: Apply for a caller that runs a provider
// directly, and Plugin for a caller driving an agent.Runner, which additionally
// gets teardown dispatched at agent.PhaseRun with the run's outcome visible.
package setup

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/shell"
)

// Apply prepares req.Setup and rewrites req to describe the prepared state:
// Cwd is where the work landed, Env is what to run it with, and Checkout is
// cleared because it has been performed.
//
// baseDir anchors relative paths in the setup; empty means the request's own Cwd,
// and failing that the process working directory. Anchoring happens here, once —
// a caller must not pre-resolve, or the second anchor is a different directory.
//
// Returns nil when the request declares no setup. Otherwise the caller owns the
// returned result's Cleanup; a Runner-driven caller should register Plugin
// instead of calling this, so teardown runs at the right phase.
func Apply(ctx context.Context, req *ai.Request, baseDir string) (*shell.SetupResult, error) {
	if req.Setup == nil {
		return nil, nil
	}
	if baseDir == "" {
		baseDir = req.Cwd()
	}
	resolved, err := req.Setup.Resolve(baseDir)
	if err != nil {
		return nil, fmt.Errorf("setup: resolve: %w", err)
	}
	// Env is an output of shell.Prepare, but a caller may also have seeded it with
	// run-specific variables. Preserve those and let them win over the prepared
	// ones, which are the defaults the environment supplies.
	declared := append([]string(nil), resolved.Env...)

	res, err := shell.Prepare(dbcontext.NewContext(ctx), &resolved)
	if err != nil {
		return nil, fmt.Errorf("setup: prepare: %w", err)
	}

	resolved.Cwd = res.Cwd
	resolved.Env = append(res.Env, declared...)
	resolved.Checkout = nil
	req.Setup = &resolved
	return res, nil
}

// Relocates reports whether a checkout would move the run into a different
// working tree — a clone, or a git worktree. It mirrors the mode inference in
// shell.Checkout's git-connection projection, so it agrees with what Apply will
// actually do rather than with what the spec literally spells out.
func Relocates(c *shell.Checkout) bool {
	if c == nil {
		return false
	}
	if c.Worktree != nil && c.Worktree.Mode != "" && c.Worktree.Mode != shell.WorktreeNone {
		return true
	}
	switch c.Mode {
	case shell.CheckoutNone:
		return false
	case "":
		return c.URL != "" || c.Path != ""
	default:
		return true
	}
}

// Plugin is Apply dispatched by an agent.Runner: it prepares the setup once
// before the loop and tears it down at agent.PhaseRun, where the run's outcome
// (HookContext.Failed / Verified) is already settled.
//
// The pre-transform request stays available to every later hook as
// HookContext.Original, so a Post hook can still ask which repo or branch the run
// was asked to work on while HookContext.Request says where that landed.
type Plugin struct {
	// BaseDir anchors relative paths in the setup; empty means the run's
	// workspace cwd.
	BaseDir string

	prepared *shell.SetupResult
}

func (p *Plugin) Name() string { return "setup" }

// IsolatesWorkspace reports whether this run's setup relocates the work into its
// own tree, so agent.EnsureSingleIsolator can catch a second isolating hook.
func (p *Plugin) IsolatesWorkspace(hc *agent.HookContext) bool {
	return hc.Request.Setup != nil && Relocates(hc.Request.Setup.Checkout)
}

// PreRun prepares the setup and points the run's workspace at the result.
func (p *Plugin) PreRun(hc *agent.HookContext) error {
	if hc.Request.Setup == nil {
		return nil
	}
	if err := hc.EnsureSingleIsolator(); err != nil {
		return err
	}
	baseDir := p.BaseDir
	if baseDir == "" {
		baseDir = hc.Workspace().Cwd
	}
	res, err := Apply(hc, hc.Request, baseDir)
	if err != nil {
		return err
	}
	p.prepared = res
	hc.Workspace().Cwd = res.Cwd
	return nil
}

// Phases declares teardown as the final phase, so a hook that commits at
// PhaseAgent still sees a live checkout.
func (p *Plugin) Phases() []agent.Phase { return []agent.Phase{agent.PhaseRun} }

// Post tears the prepared setup down. It runs even when the run failed — that is
// what makes teardown reliable — and clears its own state so a re-dispatched
// phase cannot tear the same workspace down twice.
func (p *Plugin) Post(_ *agent.HookContext, _ agent.Phase) error {
	if p.prepared == nil || p.prepared.Cleanup == nil {
		return nil
	}
	cleanup := p.prepared.Cleanup
	p.prepared = nil
	return cleanup()
}
