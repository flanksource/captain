// Package promptrun runs one resolved prompt spec through captain's
// generate→verify loop: attachment checks, provider construction, tool-policy
// enforcement, the workflow's commit and verify hooks, the caller's own hooks,
// the setup plugin, and finally the agent.Runner.
//
// It is the seam `captain prompt run` and an embedding host (gavel's todo
// lifecycle) share. Both used to assemble the same pieces by hand and drifted:
// one applied the budget timeout and the other did not, one refused an
// unenforceable tool policy before the first model call and the other let the
// provider discover it mid-run. One function means one definition of a run.
//
// What stays with the caller: rendering the prompt and resolving its spec,
// resolving attachments against a store, persisting the run, and streaming
// events to whoever is watching (OnEvent is the tap for that).
package promptrun

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/api"
)

// Input is everything one run needs.
type Input struct {
	// Request is the rendered prompt and its resolved spec. Attachments must
	// already be resolved (see api.AttachmentRef.IsPrepared): the store that
	// resolves them belongs to the caller.
	Request ai.Request
	// Config builds the provider: model, credentials, sandbox, cache, and the
	// CanUseTool broker. Ignored for construction when Provider is set.
	Config ai.Config
	// Provider, when set, is used as-is and is taken to own the workspace — a
	// remote-executing sandbox that materialises the checkout on its own side,
	// or a test double. Run then adds no setup hook. Nil means Run constructs
	// the provider from Config and prepares Request.Setup through the setup
	// plugin, in-process.
	Provider ai.Provider
	// Hooks are the caller's own agent hooks — a host's commit pipeline, an
	// environment stamp. They sit after the workflow's checks and before setup,
	// so a Post hook of theirs at PhaseRun still sees a live worktree.
	Hooks []any
	// CallerOwnsCommits hands committing to Hooks: Run builds no commit hook of
	// its own from Workflow.Commits, and leaves the declaration on the request so
	// the recorded spec still says what the run commits and validation of that
	// declaration still runs. It is for a host whose commit pipeline is its own
	// (gavel's pre-commit gates and trailers) — without it that host commits the
	// same tree twice, and stripping Workflow.Commits to avoid it drops the
	// declaration from the spec. Setting it with no Hooks is an error: nothing
	// would commit.
	CallerOwnsCommits bool
	// Verify configures the workflow's verifiers. Provider is filled from the
	// run's provider when nil, and RunSpec from the resolved request; Progress
	// receives in-flight snapshots.
	Verify verify.Options
	// OnEvent taps the live event stream: the model's events and the hooks'. It
	// takes the runner's own signature — a renderer needs the turn an event
	// belongs to, and a single-turn caller ignores it.
	OnEvent func(iter int, ev ai.Event)
	// Timeout bounds the whole run when the spec's budget.timeout is empty. There
	// is no default: a run nobody bounded is the host's configuration to fix, and
	// inventing a ceiling here killed long runs with nothing to point at.
	Timeout time.Duration
	// MaxIterations overrides the workflow's verify.maxIterations for a caller
	// that names the loop bound itself (`captain ai agent --max-iterations`);
	// zero means the workflow decides.
	MaxIterations int
	// Scope overrides the workflow's verify.scope; empty means the workflow
	// decides.
	Scope agent.Scope
	// NoStream forces buffered execution even on a streaming provider.
	NoStream bool
	// Repo is the root of the tree the run's changed files are recorded relative
	// to; empty means the request's cwd.
	Repo string
}

// Run executes one prompt run and returns its outcome. A failing verdict is a
// Result with Passed=false, not an error; an error means the run itself could
// not complete — a hook failed, the provider failed, the policy is unenforceable.
func Run(ctx context.Context, in Input) (Result, error) {
	start := time.Now()
	// One classification, the same one the runner makes: a run generates, or it
	// verifies what is already there. Anything else — attachments or a message
	// history with no prompt and nothing declared to verify — used to build a
	// provider and then quietly report a pass having done neither.
	if err := in.Request.ValidateRunnable(); err != nil {
		return Result{}, fmt.Errorf("promptrun: %w", err)
	}
	if err := validateAttachments(in); err != nil {
		return Result{}, err
	}
	timeout, err := runTimeout(in)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := requireToolPolicy(in); err != nil {
		return Result{}, err
	}
	provider, release, err := buildProvider(in)
	if err != nil {
		return Result{}, err
	}
	defer release()

	hooks, err := Hooks(ctx, in, provider)
	if err != nil {
		return Result{}, err
	}
	streamer, err := runnerProvider(provider, in.NoStream, in.Request.IsVerifyOnly())
	if err != nil {
		return Result{}, err
	}

	var identity runIdentity
	runner := &agent.Runner[string]{
		Provider:      streamer,
		Request:       in.Request,
		Hooks:         hooks,
		MaxIterations: maxIterations(in),
		Repo:          repoOf(in),
		Cwd:           in.Request.Cwd(),
		Scope:         scopeOf(in),
		OnEvent: func(iter int, ev ai.Event) {
			identity.observe(ev)
			if in.OnEvent != nil {
				in.OnEvent(iter, ev)
			}
		},
	}
	out, runErr := runner.Run(ctx)
	result, resultErr := newResult(out, identity, time.Since(start))
	if runErr != nil {
		return result, runErr
	}
	return result, resultErr
}

func maxIterations(in Input) int {
	if in.MaxIterations > 0 {
		return in.MaxIterations
	}
	return verify.MaxIterationsForWorkflow(in.Request.Workflow)
}

func scopeOf(in Input) agent.Scope {
	if in.Scope != "" {
		return in.Scope
	}
	return verify.ScopeForWorkflow(in.Request.Workflow)
}

// validateAttachments refuses a request whose attachments were never resolved
// — a path or URL the provider would have to fetch itself — and one whose
// resolved attachments the selected models cannot accept.
func validateAttachments(in Input) error {
	refs := in.Request.Prompt.Attachments
	if len(refs) == 0 {
		return nil
	}
	for i, ref := range refs {
		if !ref.IsPrepared() {
			return fmt.Errorf("promptrun: attachment %d (%s) is not resolved; resolve attachments against a store before running", i, ref.Path+ref.URL+ref.ID)
		}
	}
	model := executingModel(in)
	models := append([]api.Model{model}, model.Fallbacks...)
	return ai.ValidateAttachmentCompatibility(models, refs)
}

// executingModel is the model this run will actually be answered by: the one
// middleware.NewProvider is handed, which is Config.Model whenever the config
// names one, and the request's own model otherwise (a caller that supplied its
// provider, or a test).
//
// It exists because the two pre-flight checks resolved it differently — the
// attachment check preferred the request's model and the tool-policy check the
// config's — so a prompt whose frontmatter named a different model than the
// config had its attachments validated against a runtime that would never see
// them, and its policy against one that could not enforce it.
func executingModel(in Input) api.Model {
	if in.Config.Model.Name != "" || in.Config.Model.Provider != nil {
		return in.Config.Model
	}
	return in.Request.Model
}

// runTimeout is the spec's budget.timeout when declared, else the caller's. The
// spec wins because it is what the run's author declared; the caller's value is
// a host default.
//
// There is no third fallback. A compiled-in ceiling capped a run that had
// declared nothing, so a job that legitimately needed an hour died at two
// minutes with a deadline nobody had chosen and nothing naming it.
func runTimeout(in Input) (time.Duration, error) {
	timeout, err := in.Request.Budget.ParseTimeout()
	if err != nil {
		return 0, fmt.Errorf("promptrun: %w", err)
	}
	if timeout > 0 {
		return timeout, nil
	}
	if in.Timeout > 0 {
		return in.Timeout, nil
	}
	return 0, fmt.Errorf("promptrun: no timeout: declare budget.timeout on the spec or set Input.Timeout")
}

// requireToolPolicy refuses a per-tool policy the selected runtime cannot
// enforce before anything runs. Every provider repeats the check at execution
// time, but by then setup has materialised a checkout and a host has recorded
// a run that was never going to start.
func requireToolPolicy(in Input) error {
	model := executingModel(in)
	return api.RequireToolPolicySupport(model.Provider, model.Mode, in.Request.Permissions)
}

// buildProvider returns the caller's provider, or constructs one from Config
// when the run will call a model: a generating run always does, and a
// verify-only run does when it declares judge prompts. A verify-only run of
// commands and fixtures needs none, so none is built — it must work without
// credentials to hand.
func buildProvider(in Input) (ai.Provider, func(), error) {
	release := func() {}
	if in.Provider != nil {
		return in.Provider, release, nil
	}
	if in.Request.IsVerifyOnly() && !declaresPrompts(in.Request.Workflow) {
		return nil, release, nil
	}
	cfg := in.Config
	if in.Request.NoCache {
		cfg.NoCache = true
	}
	provider, err := middleware.NewProvider(cfg)
	if err != nil {
		return nil, release, err
	}
	return provider, func() { closeProvider(provider) }, nil
}

func declaresPrompts(wf *api.Workflow) bool {
	return wf != nil && wf.Verify != nil && len(wf.Verify.Prompts) > 0
}

func closeProvider(provider ai.Provider) {
	if closer, ok := api.ProviderAs[api.CloseableProvider](provider); ok {
		_ = closer.Close()
	}
}

func repoOf(in Input) string {
	if in.Repo != "" {
		return in.Repo
	}
	return in.Request.Cwd()
}
