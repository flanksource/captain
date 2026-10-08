package approval

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/flanksource/captain/pkg/api"
	"golang.org/x/term"
)

// TerminalOptions configures a Terminal broker.
type TerminalOptions struct {
	// In is where the operator answers; it must be a terminal.
	In *os.File
	// Out is where the request and the form are drawn.
	Out *os.File
	// Suspend, when set, pauses the host's live output while a prompt is open.
	// The returned resume restores it.
	Suspend func() (resume func())
}

// Terminal answers approvals by asking the operator at the terminal the run
// was started from. It is the interactive counterpart of Broker for a host
// that runs an agent in the foreground with no durable approval table.
//
// Requests are answered one at a time: a turn with parallel tool calls raises
// several at once, and two forms drawn over each other cannot be read.
type Terminal struct {
	suspend func() func()
	ask     func(context.Context, terminalPrompt) (terminalChoice, error)
	// turn is a one-slot semaphore rather than a mutex so a request queued
	// behind an open prompt still ends when its own context does.
	turn chan struct{}
	// allowed holds the tools the operator allowed for the rest of the run.
	// It is only touched while holding turn.
	allowed map[string]bool
}

// NewTerminal builds a Terminal over opts. It fails when In is not a terminal:
// a prompt nobody can answer would only hang the run until its deadline.
func NewTerminal(opts TerminalOptions) (*Terminal, error) {
	if opts.In == nil || !term.IsTerminal(int(opts.In.Fd())) {
		return nil, errors.New("approval: terminal broker needs a TTY on stdin")
	}
	if opts.Out == nil {
		return nil, errors.New("approval: terminal broker needs an output to draw on")
	}
	return newTerminal(opts.Suspend, huhPrompter(opts.In, opts.Out)), nil
}

func newTerminal(suspend func() func(), ask func(context.Context, terminalPrompt) (terminalChoice, error)) *Terminal {
	return &Terminal{suspend: suspend, ask: ask, turn: make(chan struct{}, 1), allowed: map[string]bool{}}
}

// OnApproval is an api.ApprovalFunc.
func (t *Terminal) OnApproval(ctx context.Context, req api.ApprovalRequest) (api.ApprovalDecision, error) {
	if err := req.Validate(); err != nil {
		return api.ApprovalDecision{}, fmt.Errorf("approval: %w", err)
	}
	prompt, err := buildTerminalPrompt(req)
	if err != nil {
		return api.ApprovalDecision{}, err
	}
	select {
	case t.turn <- struct{}{}:
	case <-ctx.Done():
		return api.ApprovalDecision{}, fmt.Errorf("approval: waiting to ask about %q: %w", req.Tool, ctx.Err())
	}
	defer func() { <-t.turn }()

	if t.allowed[req.Tool] {
		return api.ApprovalDecision{Allow: true}, nil
	}
	choice, err := t.prompt(ctx, prompt)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return api.ApprovalDecision{}, fmt.Errorf("approval: asking about %q: %w", req.Tool, ctxErr)
	}
	if err != nil {
		return api.ApprovalDecision{}, fmt.Errorf("approval: asking about %q: %w", req.Tool, err)
	}
	decision, err := t.decide(req, prompt, choice)
	if err != nil {
		return api.ApprovalDecision{}, err
	}
	if err := decision.Validate(req); err != nil {
		return api.ApprovalDecision{}, fmt.Errorf("approval: %w", err)
	}
	return decision, nil
}

func (t *Terminal) prompt(ctx context.Context, prompt terminalPrompt) (terminalChoice, error) {
	if t.suspend != nil {
		resume := t.suspend()
		if resume == nil {
			return "", errors.New("approval: terminal suspend returned no resume")
		}
		defer resume()
	}
	return t.ask(ctx, prompt)
}

// decide turns the operator's choice into a decision, refusing a choice the
// prompt did not offer.
func (t *Terminal) decide(req api.ApprovalRequest, prompt terminalPrompt, choice terminalChoice) (api.ApprovalDecision, error) {
	if !prompt.offers(choice) {
		return api.ApprovalDecision{}, fmt.Errorf("approval: %q is not a choice offered for %q", choice, req.Tool)
	}
	denied := req.Tool + " denied by the operator at the terminal"
	switch choice {
	case choiceAllowOnce:
		return api.ApprovalDecision{Allow: true}, nil
	case choiceAllowForRun:
		t.allowed[req.Tool] = true
		return api.ApprovalDecision{Allow: true}, nil
	case choiceDeny:
		return api.ApprovalDecision{Message: denied}, nil
	case choiceDenyAndStop:
		return api.ApprovalDecision{Message: denied, Interrupt: true}, nil
	}
	return api.ApprovalDecision{}, fmt.Errorf("approval: unknown terminal choice %q for %q", choice, req.Tool)
}
