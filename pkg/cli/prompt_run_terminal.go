package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/approval"
	"golang.org/x/term"
)

type promptRendererKey struct{}

func attachPromptTerminal(ctx context.Context, rendered *PromptRenderResult) (context.Context, *EventRenderer, error) {
	if rendered.Config.OnApproval != nil || rendered.Mode != "agent" ||
		(rendered.Provider != "anthropic" && rendered.Provider != "openai") ||
		(rendered.Provider == "anthropic" && rendered.Input.Permissions.Mode == "bypassPermissions") ||
		!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return ctx, nil, nil
	}
	renderer := NewEventRenderer(os.Stderr)
	terminal, err := approval.NewTerminal(approval.TerminalOptions{
		In: os.Stdin, Out: os.Stderr, Suspend: renderer.Suspend,
	})
	if err != nil {
		return ctx, nil, err
	}
	rendered.Config.OnApproval = terminal.OnApproval
	return context.WithValue(ctx, promptRendererKey{}, renderer), renderer, nil
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
