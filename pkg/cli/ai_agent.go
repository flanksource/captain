package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/commit"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/ai/agent/worktree"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
)

// AIAgentOptions runs the plugin-based agent loop (pkg/ai/agent) from the CLI:
// an iterative AI run wrapped by optional verifiers (re-run on failure), a
// throwaway git worktree, a commit step, and an LLM judge. It embeds
// AIRuntimeOptions so it inherits model/backend/tool/effort selection.
type AIAgentOptions struct {
	AIRuntimeOptions

	Prompt       string `flag:"prompt" help:"Task prompt" short:"p" required:"true" stdin:"true"`
	System       string `flag:"system" help:"System prompt" short:"s"`
	AppendSystem string `flag:"append-system" help:"Append text to the default system prompt"`
	Timeout      string `flag:"timeout" help:"Overall run timeout" default:"600s"`

	MaxIterations int      `flag:"max-iterations" help:"Max verify-and-rerun iterations" default:"1"`
	Verify        []string `flag:"verify" help:"Shell command run after each turn; non-zero exit triggers a re-run (repeatable)"`
	Scope         string   `flag:"scope" help:"Verifier scope: changed|all" default:"all"`
	Worktree      bool     `flag:"worktree" help:"Run inside a throwaway git worktree/branch"`
	Branch        string   `flag:"branch" help:"Worktree branch name (with --worktree; default: captain/agent-<ts>)"`
	Commit        bool     `flag:"commit" help:"Commit changes on the worktree branch (requires --worktree)"`
	CommitOn      string   `flag:"commit-on" help:"When --commit fires: turn|agent|run" default:"turn"`
	Squash        bool     `flag:"squash" help:"Autosquash the per-turn fixup chain into one commit; --squash=false keeps the chain" default:"true"`
	CommitMessage string   `flag:"commit-message" help:"Subject of the run's commit (default: derived from the prompt)"`
	Judge         string   `flag:"judge" help:"LLM-judge rubric; fails a turn when the judge rejects the result"`
}

type AIAgentResult struct {
	Iterations int    `json:"iterations" pretty:"label=Iterations"`
	StopReason string `json:"stopReason" pretty:"label=Stop Reason"`
	Passed     bool   `json:"passed" pretty:"label=Passed"`
	// Model, Provider and Mode are what the run actually resolved to, not what was
	// asked for: a caller handing the session to a chat thread needs the model
	// the provider answered as, and only the server can name it.
	Model        string   `json:"model,omitempty" pretty:"label=Model"`
	Provider     string   `json:"provider,omitempty" pretty:"label=Provider"`
	Mode         string   `json:"mode,omitempty" pretty:"label=Mode"`
	CostUSD      float64  `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	SessionID    string   `json:"sessionId,omitempty" pretty:"label=Session"`
	Branch       string   `json:"branch,omitempty" pretty:"label=Branch"`
	ChangedFiles []string `json:"changedFiles,omitempty" pretty:"label=Changed Files"`
	Duration     string   `json:"duration" pretty:"label=Duration"`
}

// judgeTemplate is the inline dotprompt the LLM judge renders. The rubric, cwd
// and changed-file list are supplied as data; the {ok,reason,feedback} schema is
// enforced by LLMJudgeVerifier's structured-output target.
const judgeTemplate = `{{role "system"}}
You are a strict reviewer. Decide whether the change satisfies the rubric.
Set ok=true only if it fully satisfies the rubric; otherwise ok=false with
specific, actionable feedback the agent can act on next turn.
{{role "user"}}
Rubric: {{rubric}}
Working directory: {{cwd}}
Changed files: {{changed}}`

func scopeFromFlag(s string) (agent.Scope, error) {
	return agent.ParseScope(s)
}

// buildAgentPlugins assembles the commit/verify/worktree/judge plugins from the
// flags. --commit still requires --worktree: committing into the caller's own
// checkout is a decision the CLI does not make on their behalf, even though the
// commit hook itself can do it safely (API callers reach that via api.Commit).
//
// Hook order is load-bearing at PhaseRun: the commit hook is registered ahead of
// the worktree plugin so the chain is squashed into one commit *before* the
// merge, leaving `wt merge` real commits to take rather than a dirty tree it
// would have to invent an LLM message for.
func buildAgentPlugins(ctx context.Context, opts AIAgentOptions, p ai.Provider) ([]any, *worktree.Plugin, error) {
	if opts.Commit && !opts.Worktree {
		return nil, nil, fmt.Errorf("--commit requires --worktree (captain commits the isolated branch, not your working tree)")
	}

	var hooks []any
	if opts.Commit {
		spec := api.Commit{
			On:      api.CommitPhase(opts.CommitOn),
			Message: opts.CommitMessage,
		}
		// Only the off switch is forwarded: a plain bool cannot distinguish
		// `--squash=true` from the flag's default, and passing true through would
		// make `--commit-on=run` (which commits rather than fixes up, so has no
		// chain to squash) fail validation on a value the user never typed.
		if !opts.Squash {
			spec.Squash = &opts.Squash
		}
		if err := spec.Validate(); err != nil {
			return nil, nil, err
		}
		hooks = append(hooks, commit.New(spec))
	}

	var wt *worktree.Plugin
	if opts.Worktree {
		branch := opts.Branch
		if branch == "" {
			branch = fmt.Sprintf("captain/agent-%d", time.Now().Unix())
		}
		wt = &worktree.Plugin{Branch: branch}
		if opts.Commit {
			// --commit: merge the isolated branch into trunk once the run
			// succeeds, then remove the worktree. A run that fails keeps both —
			// which only became a useful outcome now that the turns it did
			// complete are committed on the branch rather than left dirty.
			wt.Merge = worktree.WorktreeMergeOnSuccess
			wt.Cleanup = worktree.WorktreeCleanupOnMerge
		} else {
			// --worktree alone: never merge, and only clean up a worktree whose
			// changes verified — otherwise keep it around for inspection.
			wt.Cleanup = worktree.WorktreeCleanupOnVerify
		}
		hooks = append(hooks, wt)
	}

	// --verify is a workflow declaration like any other, so it is dispatched
	// through the verifier registry rather than rebuilt here. One place decides
	// how a declared check becomes a hook — including the process bounds and the
	// confinement seam a hand-built CmdVerifier silently left unset — and a kind
	// the host has claimed is honoured instead of being bypassed.
	wf := &api.Workflow{Verify: &api.Verify{Commands: opts.Verify}}
	if err := wf.Validate(); err != nil {
		return nil, nil, err
	}
	checks, err := verify.HooksFor(ctx, wf, verify.Options{Provider: p})
	if err != nil {
		return nil, nil, err
	}
	hooks = append(hooks, checks...)

	// The judge is not a registry kind: --judge carries a rubric typed on the
	// command line, while api.Verify.Prompts names .prompt files on disk. There
	// is no declaration to dispatch, so this hook is built here on purpose.
	if opts.Judge != "" {
		judge := &verify.LLMJudgeVerifier{
			Provider: p,
			Prompt:   prompt.Load(judgeTemplate),
			Data: func(cwd string, changed []string) map[string]any {
				return map[string]any{"cwd": cwd, "changed": strings.Join(changed, ", "), "rubric": opts.Judge}
			},
		}
		hooks = append(hooks, verify.New("judge", judge))
	}

	return hooks, wt, nil
}

func RunAIAgent(opts AIAgentOptions) (any, error) {
	if opts.Prompt == "" {
		return nil, fmt.Errorf("prompt text required (use --prompt or pipe via stdin)")
	}

	cfg, err := opts.ToConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Model.Name == "" {
		return nil, fmt.Errorf("no model: pass --model or run 'captain configure' to set a default")
	}
	scope, err := scopeFromFlag(opts.Scope)
	if err != nil {
		return nil, err
	}
	// Validate runtime knobs (temperature/permission-mode/effort) and
	// snapshot the base request once; Build only varies the prompt per turn.
	baseReq, err := opts.ToRequest(opts.System, opts.AppendSystem, opts.Prompt)
	if err != nil {
		return nil, err
	}

	timeout, _ := time.ParseDuration(opts.Timeout)
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	// The run's context is created before the hooks are: a factory may need it
	// (loading a judge template, reaching a host's fixture runner), and a hook
	// built outside the run's deadline is a hook the deadline never bounds.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// --judge is the one hook that needs a model of its own before the run starts,
	// because it is built here rather than from the workflow promptrun assembles.
	// Nothing else does, so nothing else pays for a provider: promptrun builds the
	// run's, with the whole middleware stack behind it.
	var judgeProvider ai.Provider
	if opts.Judge != "" {
		if judgeProvider, err = middleware.NewProvider(cfg); err != nil {
			return nil, err
		}
		defer closeProvider(judgeProvider)
	}
	hooks, _, err := buildAgentPlugins(ctx, opts, judgeProvider)
	if err != nil {
		return nil, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	baseReq.SetCwd(cwd)

	renderer := NewEventRenderer(os.Stderr)
	start := time.Now()
	// promptrun.Run is the one definition of a run: the tool-policy refusal before
	// the first model call, the setup plugin, the middleware provider and the
	// deadline all arrive with it. `captain ai agent` used to assemble those by
	// hand and had none of them.
	result, runErr := promptrun.Run(ctx, promptrun.Input{
		Request:       baseReq,
		Config:        cfg,
		Hooks:         hooks,
		MaxIterations: opts.MaxIterations,
		Scope:         scope,
		Repo:          cwd,
		Timeout:       timeout,
		OnEvent:       renderer.Handle,
	})
	renderErr := renderer.Flush()

	res := AIAgentResult{
		Duration: time.Since(start).Round(time.Millisecond).String(),
		Passed:   result.Passed,
		Model:    firstNonEmpty(result.Model, cfg.Model.Name),
	}
	runtime := firstRuntime(responseRuntime(result.Response), api.RuntimeOf(cfg.Model.Provider, cfg.Model.Mode))
	res.Provider, res.Mode = runtime.Provider, string(runtime.Mode)
	if ws := responseWorkspace(result.Response); ws != nil {
		res.ChangedFiles = ws.Changed
		res.SessionID = ws.SessionID
		res.Branch = ws.Branch
	}
	if result.Loop != nil {
		res.Iterations = len(result.Loop.Iterations)
		res.StopReason = result.Loop.StopReason
		res.CostUSD = result.Loop.TotalCost
	}
	// A failed loop/verify is surfaced through the result (Passed=false), not as
	// a command error, so --format output is still rendered. A genuine provider
	// or plugin error is returned.
	if renderErr != nil || (runErr != nil && len(result.Verdicts) == 0) {
		return res, errors.Join(runErr, renderErr)
	}
	return res, nil
}

// responseRuntime / responseWorkspace read a run's response, which is nil when
// the run failed before the runner ever built one (an unenforceable tool policy,
// a refused attachment). Reading through it unguarded panicked on exactly the
// runs whose error the caller most needs to see.
func responseRuntime(resp *ai.Response) api.Runtime {
	if resp == nil {
		return api.Runtime{}
	}
	return resp.Runtime
}

func responseWorkspace(resp *ai.Response) *api.Workspace {
	if resp == nil {
		return nil
	}
	return resp.Workspace
}
