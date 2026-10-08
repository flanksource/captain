package approval

import "context"

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
