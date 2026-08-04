// Package verify provides Verify hooks for agent.Runner: a generic command
// verifier, a function verifier, and the plumbing to scope a verifier to the
// files an agent changed. Gavel layers richer lint/test/checklist verifiers on
// top via its own fixture engine.
package verify

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
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

// DefaultCmdTimeout bounds a verify command that declares no timeout of its
// own. A hook with no bound is a denial-of-service against whatever is waiting
// on the verdict — locally a stuck loop, remotely a blocked push.
const DefaultCmdTimeout = 10 * time.Minute

// CmdVerifier runs an external command (lint, test, build, …) in the run's cwd.
// A zero exit code is a pass; a non-zero exit is a failure whose feedback is the
// tail of the combined output. When PerFile is set the changed files are
// appended to the command's arguments.
//
// The command is bounded three ways: the caller's context and Timeout cap its
// wall clock, it runs in its own process group so a kill reaches its children,
// and its output is tail-bounded as it streams rather than buffered in full.
type CmdVerifier struct {
	Cmd          string
	Args         []string
	PerFile      bool
	FeedbackTail int           // max bytes of output fed back; 0 ⇒ 4096
	Timeout      time.Duration // wall-clock bound; 0 ⇒ DefaultCmdTimeout
}

func (c *CmdVerifier) Verify(ctx context.Context, cwd string, changed []string) (Verdict, error) {
	args := append([]string(nil), c.Args...)
	if c.PerFile {
		args = append(args, changed...)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultCmdTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tail := c.FeedbackTail
	if tail <= 0 {
		tail = 4096
	}
	output := &tailBuffer{max: tail}

	cmd := exec.CommandContext(ctx, c.Cmd, args...)
	cmd.Dir = cwd
	cmd.Stdout, cmd.Stderr = output, output
	// Own process group, and cancellation kills the group: signalling only the
	// pid leaves a hook's children running after their parent is dead.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// A grandchild that survives the kill holding our output pipe must not hold
	// Wait open indefinitely.
	cmd.WaitDelay = 10 * time.Second

	err := cmd.Run()
	switch {
	case err == nil:
		return Verdict{OK: true}, nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return Verdict{
			OK:       false,
			Reason:   fmt.Sprintf("%s timed out after %s", c.Cmd, timeout),
			Feedback: output.String(),
		}, nil
	case ctx.Err() != nil:
		// The run itself is being torn down; that is not a verdict on the work.
		return Verdict{}, ctx.Err()
	}
	feedback := output.String()
	if feedback == "" {
		feedback = err.Error()
	}
	return Verdict{OK: false, Reason: c.Cmd + " failed", Feedback: feedback}, nil
}

// tailBuffer keeps the last max bytes written through it, so a chatty command
// is bounded while it streams instead of being buffered whole and truncated
// afterwards.
type tailBuffer struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		copy(b.buf, b.buf[len(b.buf)-b.max:])
		b.buf = b.buf[:b.max]
		b.truncated = true
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := strings.TrimSpace(string(b.buf))
	if b.truncated && out != "" {
		return "[output truncated]\n" + out
	}
	return out
}
