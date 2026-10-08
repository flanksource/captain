package commit

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
)

// commitSubject resolves the run's commit subject once and reuses it, so every
// commit in a chain shares the anchor's subject.
func (h *Hook) commitSubject(hc *agent.HookContext) (string, error) {
	if h.subject != "" {
		return h.subject, nil
	}
	switch {
	case h.Subject != nil:
		s, err := h.Subject(hc)
		if err != nil {
			return "", fmt.Errorf("commit: subject callback: %w", err)
		}
		h.subject = strings.TrimSpace(s)
	case h.Message != "":
		h.subject = h.Message
	default:
		h.subject = deriveSubject(hc.Request)
	}
	if h.subject == "" {
		return "", fmt.Errorf("commit: resolved an empty commit subject")
	}
	return h.subject, nil
}

// maxSubject keeps the summary line inside the width git and review tools assume.
const maxSubject = 72

// deriveSubject builds a conventional-commit subject from the run's prompt. It
// never asks a model: the subject has to exist before the first turn's commit,
// and a commit that can fail on a network call is not a durability mechanism.
func deriveSubject(req *ai.Request) string {
	summary := ""
	if req != nil {
		summary = firstLine(req.Prompt.User)
		if summary == "" {
			summary = strings.TrimSpace(req.Prompt.Source)
		}
	}
	if summary == "" {
		summary = "agent run"
	}
	subject := "chore(agent): " + summary
	if len(subject) > maxSubject {
		// Cut on a rune boundary, and budget for the ellipsis in bytes — the
		// limit git and review tools apply is a column count, not a rune count.
		const ellipsis = "…"
		cut := maxSubject - len(ellipsis)
		for cut > 0 && !utf8.RuneStart(subject[cut]) {
			cut--
		}
		subject = strings.TrimRight(subject[:cut], " ") + ellipsis
	}
	return subject
}

// firstLine is the first non-empty, non-heading line of a prompt body.
func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#> "))
		if line != "" {
			return line
		}
	}
	return ""
}
