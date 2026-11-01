package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/commons/logger"
	"golang.org/x/term"
)

type promptRendererKey struct{}

// promptTerminal is the one owner of stderr for a foreground run. The event
// renderer draws the run; a terminal approval suspends it while its form is
// open; and log output is routed through it, so none of the three is drawn
// over another. Close returns stderr to whoever owned it before.
type promptTerminal struct {
	renderer   *EventRenderer
	restoreLog io.Writer
}

// attachPromptTerminal takes stderr for a run that streams its events there or
// asks for approvals at the terminal, and returns nil for one that does
// neither. The approval form is only ever built here, suspending the renderer
// that owns the screen.
func attachPromptTerminal(ctx context.Context, rendered *PromptRenderResult, noStream bool) (context.Context, *promptTerminal, error) {
	approvals := terminalApprovals(*rendered)
	if !approvals && !streamsToTerminal(*rendered, noStream) {
		return ctx, nil, nil
	}
	renderer := NewEventRenderer(os.Stderr)
	if approvals {
		terminal, err := approval.NewTerminal(approval.TerminalOptions{
			In: os.Stdin, Out: os.Stderr, Suspend: renderer.Suspend,
		})
		if err != nil {
			return ctx, nil, err
		}
		rendered.Config.OnApproval = terminal.OnApproval
	}
	// The renderer's pane output is pinned to the writer it replaces: reading
	// the logger output back would hand the renderer its own writer.
	previous := logger.GetOutput()
	renderer.paneOutput = func() io.Writer { return previous }
	logger.SetOutput(renderer.Writer())
	return context.WithValue(ctx, promptRendererKey{}, renderer), &promptTerminal{renderer: renderer, restoreLog: previous}, nil
}

func (p *promptTerminal) Close() error {
	err := p.renderer.Flush()
	logger.SetOutput(p.restoreLog)
	if err != nil {
		return fmt.Errorf("prompt terminal: %w", err)
	}
	return nil
}

// terminalApprovals reports a run whose tool approvals are asked at this
// terminal: an agent-mode run of a provider that raises them, not bypassing
// permissions, with an operator on both stdin and stderr.
func terminalApprovals(rendered PromptRenderResult) bool {
	return rendered.Config.OnApproval == nil && rendered.Mode == "agent" &&
		(rendered.Provider == "anthropic" || rendered.Provider == "openai") &&
		(rendered.Provider != "anthropic" || rendered.Input.Permissions.Mode != "bypassPermissions") &&
		term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// streamsToTerminal reports a run whose events reach this process as they
// happen. A relocated run comes back whole; a direct call buffers a schema
// prompt, while the runner streams it.
func streamsToTerminal(rendered PromptRenderResult, noStream bool) bool {
	if noStream || relocatesRun(rendered.Config) {
		return false
	}
	return requiresRunner(rendered.Input) || !rendered.Input.Prompt.HasSchema()
}

func promptEventRenderer(ctx context.Context) *EventRenderer {
	renderer, _ := ctx.Value(promptRendererKey{}).(*EventRenderer)
	return renderer
}

func unansweredPromptQuestions(outcome *ai.TerminalOutcome) error {
	if outcome != nil && outcome.Kind == ai.TerminalOutcomeQuestions {
		return fmt.Errorf("prompt ended with %d unanswered native questions", len(outcome.Questions))
	}
	return nil
}
