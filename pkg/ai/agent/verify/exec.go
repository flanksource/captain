package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultCmdTimeout bounds a verify command that declares no timeout of its
// own. A hook with no bound is a denial-of-service against whatever is waiting
// on the verdict — locally a stuck loop, remotely a blocked push.
const DefaultCmdTimeout = 10 * time.Minute

// CommandWrapFunc rewrites a command for confined execution. It mirrors
// api.CommandWrapper's Wrap signature so a resolved sandbox adapter plugs in
// directly — hook inputs are untrusted, so a receive path must never exec
// them bare on the host (issue #40 R5.2).
type CommandWrapFunc func(ctx context.Context, cmd string, args, env []string) (string, []string, []string, error)

// execRequest is one child process a verifier runs: what to execute, where,
// under what confinement and bounds, and where its streams go.
type execRequest struct {
	Cmd     string
	Args    []string
	Dir     string
	Env     []string // nil ⇒ inherit the process's
	Wrap    CommandWrapFunc
	Timeout time.Duration // 0 ⇒ DefaultCmdTimeout
	Stdin   string
	Stdout  io.Writer
	Stderr  io.Writer
}

// execOutcome is how a child process ended. Err is the non-nil exit error of a
// command that ran and failed; a process that could not be started or was torn
// down by the caller's context is reported through runProcess's error instead.
type execOutcome struct {
	State    *os.ProcessState
	Elapsed  time.Duration
	Err      error
	TimedOut bool
}

// effectiveTimeout is the wall clock a check actually gets.
func effectiveTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultCmdTimeout
	}
	return d
}

// runProcess starts the command in its own process group, bounds it by the
// request's timeout, and reports how it ended.
//
// The bounds are the point of the helper: the caller's context and Timeout cap
// its wall clock, it runs in its own process group so a kill reaches its
// children, and a grandchild that survives the kill holding an output pipe
// cannot hold Wait open indefinitely.
//
// An error means no verdict is available: the wrapper refused, or the parent
// context ended (cancellation, or its own earlier deadline) and the run is being
// torn down — which is not a judgement on the work.
func runProcess(ctx context.Context, req execRequest) (execOutcome, error) {
	timeout := effectiveTimeout(req.Timeout)
	// The verifier's own timeout lives on a derived context; the parent is
	// consulted separately below, so a parent deadline shorter than Timeout is
	// reported as the run's cancellation, not misattributed to the command.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command, args, env, err := wrapCommand(ctx, req)
	if err != nil {
		return execOutcome{}, err
	}

	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Dir = req.Dir
	if env != nil {
		cmd.Env = env
	}
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	cmd.Stdout, cmd.Stderr = req.Stdout, req.Stderr
	// Own process group, and cancellation kills the group: signalling only the
	// pid leaves a hook's children running after their parent is dead.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second

	started := time.Now()
	runErr := cmd.Run()
	outcome := execOutcome{State: cmd.ProcessState, Elapsed: time.Since(started), Err: runErr}
	switch {
	case runErr == nil:
		return outcome, nil
	case ctx.Err() != nil:
		return execOutcome{}, ctx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		outcome.TimedOut = true
	}
	return outcome, nil
}

// wrapCommand applies the confinement seam, preserving the caller's environment
// boundary. A wrapper that supplies no environment keeps the pre-wrap one:
// leaving env nil would hand the wrapped process the full inherited environment,
// silently widening a caller's deliberately reduced Env (the git-agent hook
// path, issue #40).
func wrapCommand(ctx context.Context, req execRequest) (string, []string, []string, error) {
	if req.Wrap == nil {
		return req.Cmd, req.Args, req.Env, nil
	}
	wrapEnv := req.Env
	if wrapEnv == nil {
		wrapEnv = os.Environ()
	}
	command, args, env, err := req.Wrap(ctx, req.Cmd, req.Args, wrapEnv)
	if err != nil {
		return "", nil, nil, fmt.Errorf("wrapping %s for sandboxed execution: %w", req.Cmd, err)
	}
	if env == nil {
		env = wrapEnv
	}
	return command, args, env, nil
}

// exitCodeOf reads a finished process's status; -1 when it never ran.
func exitCodeOf(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	return state.ExitCode()
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

func newTailBuffer(max int) *tailBuffer {
	if max <= 0 {
		max = defaultFeedbackTail
	}
	return &tailBuffer{max: max}
}

// defaultFeedbackTail bounds how much of a failing check's output is fed back
// into the next iteration's prompt.
const defaultFeedbackTail = 4096

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
