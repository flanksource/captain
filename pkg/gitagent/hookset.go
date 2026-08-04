// Hook-set execution (§5): an api.Workflow run as a single-verdict check
// chain over a materialized tree. Order is commit gates, then exec verifiers,
// then prompt judges; the chain stops at the first failure and that hook's
// output is the verdict's feedback (R5.1). Exec hooks are agent-authored
// input and MUST run confined — a wrap-command sandbox is required, not
// optional (R5.2/H1). Nothing here returns a Go error: every failure mode
// folds into the verdict, and an indeterminate verdict rejects (R7.5).
package gitagent

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	agentcommit "github.com/flanksource/captain/pkg/ai/agent/commit"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
)

// DefaultHookTimeout bounds one hook's wall clock inside a blocked push
// (R5.5); tighter than verify.DefaultCmdTimeout because a push is waiting.
const DefaultHookTimeout = 5 * time.Minute

// HookWorkspace is the materialized tree a hook set runs against.
type HookWorkspace struct {
	Dir     string   // absolute path of the materialized tree
	Changed []string // repo-relative paths the push changed
}

// HookSetOptions configures one tier's hook-set run.
type HookSetOptions struct {
	Workflow *api.Workflow
	Tier     string // "sidecar" | "supervisor"
	Task     string
	Attempt  int
	Depth    int         // envelope depth of the push being vetted (R5.4/H15)
	Judge    ai.Provider // provider for prompt hooks; nil forbids them
	Wrap     verify.CommandWrapFunc
	Env      []string // scrubbed environment for exec hooks (R1.1)
	Timeout  time.Duration
}

// RunHookSet executes the workflow's checks and renders the tier's verdict.
func RunHookSet(ctx context.Context, ws HookWorkspace, opts HookSetOptions) TierVerdict {
	verdict := TierVerdict{
		V:       ProtocolVersion,
		Task:    opts.Task,
		Attempt: opts.Attempt,
		Status:  StatusAccepted,
		Tier:    opts.Tier,
	}
	wf := opts.Workflow
	if wf == nil {
		return verdict
	}
	if finding, failed := runCommitGates(ws, wf); failed {
		verdict.Status = StatusRejected
		verdict.Findings = append(verdict.Findings, finding)
		return verdict
	}
	plugins, errFinding := buildHookPlugins(wf, opts)
	if errFinding != nil {
		verdict.Status = StatusError
		verdict.Findings = append(verdict.Findings, *errFinding)
		return verdict
	}
	changed := ws.Changed
	if verify.ScopeForWorkflow(wf) != agent.ScopeChanged {
		changed = nil // whole-tree semantics, matching verify.Plugin
	}
	for _, p := range plugins {
		vd, err := p.Verifier().Verify(ctx, ws.Dir, changed)
		if err != nil {
			verdict.Status = StatusError
			verdict.Findings = append(verdict.Findings, Finding{
				Hook: p.Name(), Kind: hookKind(p.Name()),
				Message: fmt.Sprintf("hook could not reach a verdict: %v", err),
			})
			return verdict
		}
		if !vd.OK {
			verdict.Status = StatusRejected
			verdict.Findings = append(verdict.Findings, Finding{
				Hook: p.Name(), Kind: hookKind(p.Name()),
				Message: vd.Reason, Feedback: vd.Feedback,
			})
			return verdict
		}
	}
	return verdict
}

// runCommitGates applies each declared commit policy's content gates over the
// changed paths (the receive-side analogue of commit.Hook's pre-commit check).
func runCommitGates(ws HookWorkspace, wf *api.Workflow) (Finding, bool) {
	for _, c := range wf.Commits {
		if err := agentcommit.CheckGates(ws.Dir, c.EffectiveGates(), 0, ws.Changed); err != nil {
			return Finding{
				Hook: "gate:commit", Kind: "commit",
				Message: err.Error(),
			}, true
		}
	}
	return Finding{}, false
}

// buildHookPlugins assembles exec and prompt verifiers via the same builders
// the local run path uses (A5.1: one hook machinery), confining every exec
// hook and bounding prompt-hook recursion.
func buildHookPlugins(wf *api.Workflow, opts HookSetOptions) ([]*verify.Plugin, *Finding) {
	var plugins []*verify.Plugin
	execHooks := verify.HooksForWorkflow(wf)
	if len(execHooks) > 0 && opts.Wrap == nil {
		return nil, &Finding{
			Hook: "hookset", Kind: "exec",
			Message: "exec hooks require a wrap-command sandbox; refusing to run agent-authored commands on the host (R5.2)",
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	for _, h := range execHooks {
		p, ok := h.(*verify.Plugin)
		if !ok {
			return nil, &Finding{Hook: "hookset", Kind: "exec", Message: fmt.Sprintf("unexpected hook type %T", h)}
		}
		if cv, ok := p.Verifier().(*verify.CmdVerifier); ok {
			cv.Env = opts.Env
			cv.Wrap = opts.Wrap
			cv.Timeout = timeout
		}
		plugins = append(plugins, p)
	}
	if wf.Verify != nil && len(wf.Verify.Prompts) > 0 && opts.Depth+1 > MaxHookDepth {
		return nil, &Finding{
			Hook: "hookset", Kind: "prompt",
			Message: fmt.Sprintf("prompt hooks at depth %d exceed the recursion bound %d (R5.4/H15)", opts.Depth+1, MaxHookDepth),
		}
	}
	judgeHooks, err := verify.PromptHooksForWorkflow(wf, opts.Judge)
	if err != nil {
		return nil, &Finding{Hook: "hookset", Kind: "prompt", Message: err.Error()}
	}
	for _, h := range judgeHooks {
		p, ok := h.(*verify.Plugin)
		if !ok {
			return nil, &Finding{Hook: "hookset", Kind: "prompt", Message: fmt.Sprintf("unexpected hook type %T", h)}
		}
		plugins = append(plugins, p)
	}
	return plugins, nil
}

func hookKind(name string) string {
	switch {
	case len(name) > 6 && name[:6] == "judge:":
		return "prompt"
	case len(name) > 7 && name[:7] == "verify:":
		return "exec"
	default:
		return "exec"
	}
}
