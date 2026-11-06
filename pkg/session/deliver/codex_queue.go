package deliver

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/clicky"
)

const codexQueueTimeout = 30 * time.Second

// queueCodex queues text on a codex thread with `codex queue`, which the live
// codex session picks up as its next message.
func (d Deliverer) queueCodex(ctx context.Context, thread, text string) error {
	run := d.Codex
	if run == nil {
		run = runCodex
	}
	if err := run(ctx, "queue", "--thread", thread, "--message", text); err != nil {
		return fmt.Errorf("codex queue --thread %s: %w", thread, err)
	}
	return nil
}

func runCodex(ctx context.Context, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := clicky.Exec("codex", args...).WithTimeout(codexQueueTimeout).Run().Result()
	if result.Error != nil {
		return result.Error
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("exit code %d: %s", result.ExitCode, result.Stderr)
	}
	return nil
}
