// Package verify provides Verify hooks for agent.Runner: a generic command
// verifier, a function verifier, and the plumbing to scope a verifier to the
// files an agent changed. Gavel layers richer lint/test/checklist verifiers on
// top via its own fixture engine.
package verify

import (
	"context"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/clicky/exec"
)

// Verdict is a Verifier's judgement. Feedback is fed back into the next
// iteration's prompt when OK is false.
type Verdict struct {
	OK       bool
	Reason   string
	Feedback string
}

// Verifier checks the state of a working tree after an agent turn. cwd is where
// the run executed; changed is the agent's changed files when the run scope is
// ScopeChanged, and nil otherwise (meaning "verify the whole tree").
type Verifier interface {
	Verify(ctx context.Context, cwd string, changed []string) (Verdict, error)
}

// Plugin adapts a Verifier into an agent.Verify hook: it scopes the verifier,
// and on failure returns the exact next request (feedback appended to the prompt)
// as the loop's Retry.
type Plugin struct {
	name string
	v    Verifier
}

// New wraps a Verifier as a named agent.Verify hook.
func New(name string, v Verifier) *Plugin { return &Plugin{name: name, v: v} }

func (p *Plugin) Name() string { return p.name }

func (p *Plugin) Verify(hc *agent.HookContext) (agent.VerifyResult, error) {
	ws := hc.Workspace()
	var changed []string
	if hc.Scope == agent.ScopeChanged {
		changed = ws.Changed
	}
	vd, err := p.v.Verify(hc, ws.Cwd, changed)
	if err != nil {
		return agent.VerifyResult{}, err
	}
	if vd.OK {
		return agent.VerifyResult{Valid: true, Output: vd}, nil
	}
	return agent.VerifyResult{Valid: false, Retry: retryWithFeedback(hc.Request, vd.Feedback), Output: vd}, nil
}

// retryWithFeedback builds the next request: the current one with the verifier's
// feedback appended to the user prompt.
func retryWithFeedback(req *ai.Request, feedback string) *ai.Request {
	next := *req
	next.Prompt.User = strings.TrimRight(req.Prompt.User, "\n") +
		"\n\n[verifier feedback]\n" + feedback + "\n\nFix the issues above and continue."
	return &next
}

// FuncVerifier adapts a function to the Verifier interface.
type FuncVerifier func(ctx context.Context, cwd string, changed []string) (Verdict, error)

func (f FuncVerifier) Verify(ctx context.Context, cwd string, changed []string) (Verdict, error) {
	return f(ctx, cwd, changed)
}

// CmdVerifier runs an external command (lint, test, build, …) in the run's cwd.
// A zero exit code is a pass; a non-zero exit is a failure whose feedback is the
// tail of the combined output. When PerFile is set the changed files are
// appended to the command's arguments.
type CmdVerifier struct {
	Cmd          string
	Args         []string
	PerFile      bool
	FeedbackTail int // max bytes of output fed back; 0 ⇒ 4096
}

func (c *CmdVerifier) Verify(_ context.Context, cwd string, changed []string) (Verdict, error) {
	args := append([]string(nil), c.Args...)
	if c.PerFile {
		args = append(args, changed...)
	}
	res := exec.NewExec(c.Cmd, args...).WithCwd(cwd).Run().Result()
	if res.Error == nil && res.ExitCode == 0 {
		return Verdict{OK: true}, nil
	}

	tail := c.FeedbackTail
	if tail <= 0 {
		tail = 4096
	}
	out := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	if len(out) > tail {
		out = out[len(out)-tail:]
	}
	return Verdict{OK: false, Reason: c.Cmd + " failed", Feedback: out}, nil
}
