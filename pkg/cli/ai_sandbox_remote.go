package cli

import (
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/sandbox/adapter"
)

// remoteAwareTimeout sizes the run's deadline for where the work happens. A
// relocated run blocks on a remote agent for as long as that agent takes, so
// the local request default — sized for a model call — would kill a dispatch
// that is still progressing and report it as a failure. An explicitly declared
// timeout (--timeout or budget.timeout) always wins.
func remoteAwareTimeout(req ai.Request, cfg ai.Config, timeout time.Duration) time.Duration {
	if strings.TrimSpace(req.Budget.Timeout) != "" || !promptrun.Relocates(cfg) {
		return timeout
	}
	return adapter.WaitTimeout(cfg.SandboxSelection.Options)
}

// renderedTimeout is remoteAwareTimeout for an already-rendered prompt, used
// by the stream and batch paths that size their own deadline. A spec that
// declares no budget.timeout gets the CLI default; one that declares an
// unparseable value is an error, not a run on a substituted deadline.
func renderedTimeout(rendered PromptRenderResult) (time.Duration, error) {
	declared, err := runtimeTimeout(rendered.Input.Budget.Timeout)
	if err != nil {
		return 0, err
	}
	if declared <= 0 {
		declared = defaultRunTimeout
	}
	return remoteAwareTimeout(rendered.Input, rendered.Config, declared), nil
}
