package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/huh"
	"github.com/flanksource/captain/pkg/api"
)

// terminalChoice is one answer the operator can pick.
type terminalChoice string

const (
	choiceAllowOnce   terminalChoice = "allow-once"
	choiceAllowForRun terminalChoice = "allow-for-run"
	choiceDeny        terminalChoice = "deny"
	choiceDenyAndStop terminalChoice = "deny-and-stop"
)

// maxPromptDetail bounds the request detail drawn above the form. A tool input
// can be a whole file; drawing all of it pushes the choices off the screen.
const maxPromptDetail = 2048

// terminalPrompt is what the operator is shown: the request and the choices it
// can take.
type terminalPrompt struct {
	Tool      string
	Kind      api.ApprovalKind
	Reason    string
	Escalates bool
	Detail    string
	Choices   []terminalChoice
}

func (p terminalPrompt) offers(choice terminalChoice) bool {
	return slices.Contains(p.Choices, choice)
}

// buildTerminalPrompt describes req for the operator, refusing the kinds that
// need an answer rather than a yes or no.
func buildTerminalPrompt(req api.ApprovalRequest) (terminalPrompt, error) {
	var payload any
	rememberable := true
	switch req.Kind {
	case api.ApprovalKindQuestion, api.ApprovalKindElicitation:
		return terminalPrompt{}, fmt.Errorf("approval: the terminal broker cannot answer a %s request from %q: it only allows or denies", req.Kind, req.Tool)
	case api.ApprovalKindTool:
		if len(req.Input) > 0 {
			payload = req.Input
		}
	case api.ApprovalKindCommand:
		payload = req.Command
	case api.ApprovalKindFilesystem:
		payload = req.Filesystem
	case api.ApprovalKindNetwork:
		payload = req.Network
	case api.ApprovalKindPermissions:
		payload, rememberable = req.Permissions, false
	case api.ApprovalKindPlan:
		payload, rememberable = req.Plan.Content, false
	default:
		return terminalPrompt{}, fmt.Errorf("approval: the terminal broker has no prompt for a %s request from %q", req.Kind, req.Tool)
	}
	detail, err := promptDetail(payload)
	if err != nil {
		return terminalPrompt{}, fmt.Errorf("approval: describe %s request from %q: %w", req.Kind, req.Tool, err)
	}
	choices := []terminalChoice{choiceAllowOnce}
	if rememberable {
		choices = append(choices, choiceAllowForRun)
	}
	choices = append(choices, choiceDeny)
	// An interrupt on a request that cannot take one is an invalid decision.
	if req.Interruptible {
		choices = append(choices, choiceDenyAndStop)
	}
	return terminalPrompt{
		Tool: req.Tool, Kind: req.Kind, Reason: req.Reason, Escalates: req.Escalates,
		Detail: clipDetail(detail), Choices: choices,
	}, nil
}

func promptDetail(payload any) (string, error) {
	switch value := payload.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	return string(data), err
}

func clipDetail(detail string) string {
	if len(detail) <= maxPromptDetail {
		return detail
	}
	end := maxPromptDetail
	for end > 0 && !utf8.RuneStart(detail[end]) {
		end--
	}
	return fmt.Sprintf("%s\n… clipped, showing %d of %d bytes", detail[:end], end, len(detail))
}

func (c terminalChoice) label(tool string) string {
	switch c {
	case choiceAllowOnce:
		return "Allow once"
	case choiceAllowForRun:
		return "Allow " + tool + " for the rest of this run"
	case choiceDeny:
		return "Deny"
	case choiceDenyAndStop:
		return "Deny and stop the turn"
	}
	return string(c)
}

// huhPrompter draws the request on out and asks for a choice on in.
func huhPrompter(in, out *os.File) func(context.Context, terminalPrompt) (terminalChoice, error) {
	return func(ctx context.Context, prompt terminalPrompt) (terminalChoice, error) {
		if err := writePromptHeader(out, prompt); err != nil {
			return "", err
		}
		options := make([]huh.Option[terminalChoice], len(prompt.Choices))
		for i, choice := range prompt.Choices {
			options[i] = huh.NewOption(choice.label(prompt.Tool), choice)
		}
		var choice terminalChoice
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[terminalChoice]().
				Title(fmt.Sprintf("Allow %s?", prompt.Tool)).
				Options(options...).
				Value(&choice),
		)).WithInput(in).WithOutput(out)
		if err := form.RunWithContext(ctx); err != nil {
			return "", err
		}
		return choice, nil
	}
}

func writePromptHeader(out io.Writer, prompt terminalPrompt) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\nApproval needed: %s (%s)\n", prompt.Tool, prompt.Kind)
	if prompt.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", prompt.Reason)
	}
	if prompt.Escalates {
		b.WriteString("Approving this exceeds the sandbox the run started with.\n")
	}
	if prompt.Detail != "" {
		b.WriteString(prompt.Detail + "\n")
	}
	_, err := io.WriteString(out, b.String())
	return err
}
