package deliver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"time"
)

const codexQueueTimeout = 30 * time.Second

var errCodexQueueUnavailable = errors.New("codex queue unavailable")

func (d Deliverer) queueCodex(ctx context.Context, target Target, text string) error {
	run := d.Codex
	if run == nil {
		run = runCodex
	}
	if err := run(ctx, target.Environment[EnvCodexHome], "queue", "--thread", target.SessionID, "--message="+text); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("%w: %w", errCodexQueueUnavailable, err)
		}
		return fmt.Errorf("codex queue --thread %s: %w", target.SessionID, err)
	}
	return nil
}

func runCodex(ctx context.Context, codexHome string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, codexQueueTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", args...)
	if codexHome != "" {
		cmd.Env = append(cmd.Environ(), EnvCodexHome+"="+codexHome)
	}
	_, err := cmd.Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		code, stderr := exit.ExitCode(), strings.TrimSpace(string(exit.Stderr))
		err = fmt.Errorf("exit code %d: %s", code, stderr)
		if codexQueueRejected(code, stderr) {
			return fmt.Errorf("%w: %w", errCodexQueueUnavailable, err)
		}
	}
	return err
}

// These CLI and server rejections occur before the queue entry is written.
func codexQueueRejected(exitCode int, stderr string) bool {
	for line := range strings.Lines(stderr) {
		line = strings.TrimSpace(line)
		if exitCode == 2 && line == "error: unrecognized subcommand 'queue'" {
			return true
		}
		if exitCode == 1 && (strings.HasPrefix(line, "Error: the local app-server daemon does not support thread/queue/add;") ||
			strings.HasPrefix(line, "Error: the remote app server does not support thread/queue/add;") ||
			strings.HasPrefix(line, "Error: failed to queue session message: thread/queue/add failed: failed to read thread: invalid thread-store request: no rollout found for thread id ")) {
			return true
		}
	}
	return false
}
