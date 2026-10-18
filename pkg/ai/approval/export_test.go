package approval

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
)

type (
	TerminalPrompt = terminalPrompt
	TerminalChoice = terminalChoice
)

const (
	ChoiceAllowOnce   = choiceAllowOnce
	ChoiceAllowForRun = choiceAllowForRun
	ChoiceDeny        = choiceDeny
	ChoiceDenyAndStop = choiceDenyAndStop
	MaxPromptDetail   = maxPromptDetail
)

// NewTestTerminal builds a Terminal whose prompter is ask instead of a huh
// form, so specs can answer without a TTY.
func NewTestTerminal(suspend func() func(), ask func(context.Context, TerminalPrompt) (TerminalChoice, error)) *Terminal {
	return newTerminal(suspend, ask)
}

// SetTestAnswerer replaces the terminal's question form with answer.
func SetTestAnswerer(t *Terminal, answer func(context.Context, api.TerminalQuestion) (any, error)) {
	t.answer = answer
}
