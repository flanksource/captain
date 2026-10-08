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
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
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

	// The checkout the run was pointed at, captured before Prepare replaces the
	// cwd with the worktree it creates and the checkout is cleared below. This is
	// the only point where both are in hand.
	origin := strings.TrimSpace(resolved.Cwd)
	if origin == "" {
		origin = strings.TrimSpace(baseDir)
	}
	intoWorktree := resolved.Checkout != nil && resolved.Checkout.Worktree != nil &&
		resolved.Checkout.Worktree.Mode != "" && resolved.Checkout.Worktree.Mode != shell.WorktreeNone

	res, err := shell.Prepare(dbcontext.NewContext(ctx), &resolved)
	if err != nil {
		return nil, fmt.Errorf("setup: prepare: %w", err)
	}

	// A worktree is not a self-contained copy: `git worktree add` brings no
	// gitignored content, so the run reaches its parent checkout for installed
	// dependencies, build output and anything else .gitignore hides. Granting the
	// origin here means the worktree travels with the directories it needs, for
	// every caller, instead of each one remembering to configure it — and a
	// headless run never stalls on a permission request nobody is there to answer.
	if intoWorktree && origin != "" && res.Cwd != "" && res.Cwd != origin {
		req.Permissions.Directories = append(req.Permissions.Directories, origin)
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
	// relocation describes the tree PreRun moved the run into, so teardown can
	// report what became of it; nil when the setup relocated nothing.
	relocation *relocation
	// worktree is the recorded state of the worktree the run was moved into —
	// the same value the run's Workspace carries — which teardown completes. Nil
	// when there is none, or PreRun failed before recording it.
	worktree *api.WorktreeState
}

type relocation struct {
	kind, path string
	// keep is the checkout's explicit request to leave the worktree in place;
	// existing marks a worktree the run was pointed at rather than one setup
	// created, which teardown never removes either. existingBranch marks a
	// branch the run continued on: it predates the run, so teardown never
	// deletes it.
	keep, existing, existingBranch bool
}

// retainReason is why teardown must keep the worktree whatever its state; empty
// means its git state decides.
func (r *relocation) retainReason() string {
	switch {
	case r.keep:
		return keptKeepRequested
	case r.existing:
		return keptExisting
	}
	return ""
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
	// Read before Apply, which consumes the checkout it performs.
	checkout := hc.Request.Setup.Checkout
	res, err := Apply(hc, hc.Request, baseDir)
	if err != nil {
		return err
	}
	p.prepared = res
	hc.Workspace().Cwd = res.Cwd
	if !Relocates(checkout) {
		return nil
	}
	p.relocation = &relocation{kind: "checkout", path: res.Cwd}
	wt := checkout.Worktree
	if wt == nil || wt.Mode == "" || wt.Mode == shell.WorktreeNone {
		hc.Notify("[pre-run] checkout %s", res.Cwd)
		return nil
	}
	p.relocation.kind = "worktree"
	p.relocation.keep, p.relocation.existing = wt.Keep, wt.Mode == shell.WorktreeExisting
	p.relocation.existingBranch = wt.Mode == shell.WorktreeBranch
	// Only a worktree setup created holds a copy of the source's work-in-progress;
	// an existing one's uncommitted state is its owner's, and never committed here.
	state, err := recordWorktree(res.Cwd, wt.Mode == shell.WorktreeNew)
	if err != nil {
		return err
	}
	// Worktree is how later hooks tell an isolated tree from the caller's own
	// checkout — the commit hook stages the whole tree only when it is set — so a
	// worktree that does not record one reads as shared.
	p.worktree = state
	ws := hc.Workspace()
	ws.Worktree, ws.Repo = state, state.Repo
	hc.Notify("[pre-run] worktree %s on %s from %s", res.Cwd, state.Branch, shortSHA(state.Base))
	return nil
}

// Phases declares teardown as the final phase, so a hook that commits at
// PhaseAgent still sees a live checkout.
func (p *Plugin) Phases() []agent.Phase { return []agent.Phase{agent.PhaseRun} }

// Post tears the prepared setup down. It runs even when the run failed — that is
// what makes teardown reliable — and clears its own state so a re-dispatched
// phase cannot tear the same workspace down twice.
//
// A recorded worktree's fate is decided by its git state (teardownWorktree): it
// runs after the commit hooks, so a failed or skipped commit leaves a dirty tree
// that is kept rather than force-removed with the agent's edits in it.
func (p *Plugin) Post(hc *agent.HookContext, _ agent.Phase) error {
	if p.prepared == nil {
		return nil
	}
	cleanup, moved, wt := p.prepared.Cleanup, p.relocation, p.worktree
	p.prepared, p.relocation, p.worktree = nil, nil, nil
	if wt != nil {
		if err := teardownWorktree(wt, moved, cleanup); err != nil {
			return err
		}
		hc.Notify("%s", teardownNotice(wt))
		return nil
	}
	if cleanup != nil {
		if err := cleanup(); err != nil {
			return err
		}
	}
	if moved != nil {
		verb := "removed"
		if moved.keep || cleanup == nil {
			verb = "kept"
		}
		hc.Notify("[post-run] %s %s %s", verb, moved.kind, moved.path)
	}
	return nil
}
