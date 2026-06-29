package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/ai/agent/worktree"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/ai/prompt"
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
	Judge         string   `flag:"judge" help:"LLM-judge rubric; fails a turn when the judge rejects the result"`
}

type AIAgentResult struct {
	Iterations   int      `json:"iterations" pretty:"label=Iterations"`
	StopReason   string   `json:"stopReason" pretty:"label=Stop Reason"`
	Passed       bool     `json:"passed" pretty:"label=Passed"`
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

// buildAgentPlugins assembles the verify/worktree/judge plugins from the flags.
// The worktree plugin owns the commit (it commits the branch on teardown), so
// --commit requires --worktree.
func buildAgentPlugins(opts AIAgentOptions, p ai.Provider) ([]agent.Plugin, *worktree.Plugin, error) {
	if opts.Commit && !opts.Worktree {
		return nil, nil, fmt.Errorf("--commit requires --worktree (captain commits the isolated branch, not your working tree)")
	}

	var plugins []agent.Plugin
	var wt *worktree.Plugin
	if opts.Worktree {
		branch := opts.Branch
		if branch == "" {
			branch = fmt.Sprintf("captain/agent-%d", time.Now().Unix())
		}
		wt = &worktree.Plugin{Branch: branch}
		if opts.Commit {
			wt.CommitMsg = "captain: " + commitSubject(opts.Prompt)
		}
		plugins = append(plugins, wt)
	}

	for _, cmd := range opts.Verify {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		plugins = append(plugins, verify.New("verify:"+cmd, &verify.CmdVerifier{Cmd: "sh", Args: []string{"-c", cmd}}))
	}

	if opts.Judge != "" {
		judge := &verify.LLMJudgeVerifier{
			Provider: p,
			Prompt:   prompt.Load(judgeTemplate),
			Data: func(cwd string, changed []string) map[string]any {
				return map[string]any{"cwd": cwd, "changed": strings.Join(changed, ", "), "rubric": opts.Judge}
			},
		}
		plugins = append(plugins, verify.New("judge", judge))
	}

	return plugins, wt, nil
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

	p, err := ai.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	if p, err = middleware.Wrap(p, middleware.WithLogging()); err != nil {
		return nil, err
	}
	sp, ok := p.(ai.StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("backend %s does not support the streaming agent loop", cfg.Model.Backend)
	}

	plugins, wt, err := buildAgentPlugins(opts, p)
	if err != nil {
		return nil, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	renderer := newLineRenderer(os.Stderr, 8)
	runner := &agent.Runner{
		Provider: sp,
		Plugins:  plugins,
		Loop: ai.LoopOptions{
			MaxIterations: opts.MaxIterations,
			OnEvent:       func(_ int, ev ai.Event) { renderEvent(os.Stderr, renderer, ev) },
		},
		Build: func(_ *agent.RunContext, _ int, _ *ai.LoopIteration, feedback string) ai.Request {
			req := baseReq
			if feedback != "" {
				req.Prompt.User = opts.Prompt + "\n\n[verifier feedback]\n" + feedback + "\n\nFix the issues above and continue."
			}
			return req
		},
		Repo:  cwd,
		Cwd:   cwd,
		Scope: scope,
	}

	timeout, _ := time.ParseDuration(opts.Timeout)
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	result, runErr := runner.Run(ctx)
	if result == nil {
		return nil, runErr
	}

	res := AIAgentResult{
		ChangedFiles: result.ChangedFiles,
		SessionID:    result.SessionID,
		Duration:     time.Since(start).Round(time.Millisecond).String(),
		Passed:       verdictsPassed(result.Verdicts, runErr),
	}
	if result.Loop != nil {
		res.Iterations = len(result.Loop.Iterations)
		res.StopReason = result.Loop.StopReason
		res.CostUSD = result.Loop.TotalCost
	}
	if wt != nil && wt.Result != nil {
		res.Branch = wt.Result.Branch
	}
	// A failed loop/verify is surfaced through the result (Passed=false), not as
	// a command error, so --format output is still rendered. A genuine provider
	// or plugin error is returned.
	if runErr != nil && len(result.Verdicts) == 0 {
		return res, runErr
	}
	return res, nil
}

// verdictsPassed reports whether the last verifier verdict was OK. With no
// verifiers, the run passed when the loop returned no error.
func verdictsPassed(verdicts []agent.Verdict, runErr error) bool {
	if len(verdicts) == 0 {
		return runErr == nil
	}
	return verdicts[len(verdicts)-1].OK
}

// commitSubject derives a one-line, length-capped commit subject from the prompt.
func commitSubject(s string) string {
	s = strings.TrimSpace(firstLine(s))
	if len(s) > 72 {
		s = s[:72]
	}
	return s
}
