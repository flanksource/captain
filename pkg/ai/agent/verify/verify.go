// Package verify provides VerifyPlugins for agent.Runner: a generic command
// verifier, a function verifier, and the plumbing to scope a verifier to the
// files an agent changed. Gavel layers richer lint/test verifiers on top.
package verify

import (
	"context"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/clicky/exec"
)

// Verifier checks the state of a working tree after an agent turn. cwd is where
// the run executed; changed is the agent's changed files when the run scope is
// ScopeChanged, and nil otherwise (meaning "verify the whole tree").
type Verifier interface {
	Verify(ctx context.Context, cwd string, changed []string) (agent.Verdict, error)
}

// Plugin adapts a Verifier into an agent.VerifyPlugin, applying the run's scope.
type Plugin struct {
	name string
	v    Verifier
}

// New wraps a Verifier as a named VerifyPlugin.
func New(name string, v Verifier) *Plugin { return &Plugin{name: name, v: v} }

func (p *Plugin) Name() string { return p.name }

// Verify passes the changed-file set to the inner verifier only when the run is
// scoped to changed files; otherwise it passes nil ("whole tree").
func (p *Plugin) Verify(rc *agent.RunContext, _ *ai.LoopIteration) (agent.Verdict, error) {
	var changed []string
	if rc.Scope == agent.ScopeChanged {
		changed = rc.ChangedFiles
	}
	return p.v.Verify(rc.Ctx, rc.Cwd, changed)
}

// FuncVerifier adapts a function to the Verifier interface.
type FuncVerifier func(ctx context.Context, cwd string, changed []string) (agent.Verdict, error)

func (f FuncVerifier) Verify(ctx context.Context, cwd string, changed []string) (agent.Verdict, error) {
	return f(ctx, cwd, changed)
}

// CmdVerifier runs an external command (lint, test, build, …) in the run's cwd.
// A zero exit code is a pass; a non-zero exit is a failure whose feedback is the
// tail of the combined output. When PerFile is set the changed files are
// appended to the command's arguments (for tools that take explicit paths).
type CmdVerifier struct {
	Cmd          string
	Args         []string
	PerFile      bool
	FeedbackTail int // max bytes of output fed back; 0 ⇒ 4096
}

func (c *CmdVerifier) Verify(_ context.Context, cwd string, changed []string) (agent.Verdict, error) {
	args := append([]string(nil), c.Args...)
	if c.PerFile {
		args = append(args, changed...)
	}
	res := exec.NewExec(c.Cmd, args...).WithCwd(cwd).Run().Result()
	if res.Error == nil && res.ExitCode == 0 {
		return agent.Verdict{OK: true}, nil
	}

	tail := c.FeedbackTail
	if tail <= 0 {
		tail = 4096
	}
	out := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	if len(out) > tail {
		out = out[len(out)-tail:]
	}
	return agent.Verdict{
		OK:       false,
		Reason:   c.Cmd + " failed",
		Feedback: out,
	}, nil
}
