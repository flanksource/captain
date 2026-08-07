package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
)

// sessionTitleInstruction rides the system prompt whenever the naming tool is
// exposed. Without it the model almost never volunteers a title, and the
// conversation falls back to its opening message.
const sessionTitleInstruction = "Call " + session.TitleToolName +
	" once, as early as you can, with a short title (at most eight words) describing what this conversation is about."

const sessionTitleInput = "aiTitle"

// appendSessionTitleInstruction adds the naming instruction to whichever system
// prompt the request carries. The two request modes are mutually exclusive, so
// message-mode specs must stay message-mode: agent backends prompt through
// Prompt, everything else through a leading system message.
func appendSessionTitleInstruction(spec *api.Spec) {
	if len(spec.Messages) == 0 {
		spec.Prompt.AppendSystem = strings.TrimSpace(spec.Prompt.AppendSystem + "\n\n" + sessionTitleInstruction)
		return
	}
	if spec.Messages[0].Role == api.RoleSystem {
		parts := spec.Messages[0].Parts
		if len(parts) > 0 && parts[len(parts)-1].Type == api.PartText {
			parts[len(parts)-1].Text += "\n\n" + sessionTitleInstruction
			return
		}
	}
	spec.Messages = append([]api.Message{{
		Role: api.RoleSystem, Parts: []api.Part{{Type: api.PartText, Text: sessionTitleInstruction}},
	}}, spec.Messages...)
}

func normalizeTitle(update TitleUpdate) (string, error) {
	title := strings.Join(strings.Fields(update.Title), " ")
	if title == "" {
		return "", fmt.Errorf("chat thread title cannot be empty")
	}
	if titleRank(update.Source) == 0 {
		return "", fmt.Errorf("unknown chat thread title source %q", update.Source)
	}
	return title, nil
}

func titleRank(source TitleSource) int {
	switch source {
	case TitleSourceDerived:
		return 1
	case TitleSourceAI:
		return 2
	case TitleSourceUser:
		return 3
	default:
		return 0
	}
}

// titleWins applies the naming precedence: a person's title is final, the
// agent's replaces one inferred from the opening message, and an inferred title
// only ever fills a blank.
func titleWins(current string, stored, incoming TitleSource) bool {
	if strings.TrimSpace(current) == "" {
		return true
	}
	return titleRank(incoming) > titleRank(stored)
}

// derivedTitle names a conversation after the first thing the user asked.
func derivedTitle(messages []UIMessage) string {
	for _, message := range messages {
		if !strings.EqualFold(message.Role, string(api.RoleUser)) {
			continue
		}
		for _, part := range message.Parts {
			if part.Type != "text" {
				continue
			}
			if title := session.DeriveTitle(part.Text); title != "" {
				return title
			}
		}
	}
	return ""
}

// agentTitle reads the title an agent gave itself, from either the tool Captain
// exposes or an equivalent call the backend emits on its own.
func agentTitle(message UIMessage) string {
	title := ""
	for _, part := range message.Parts {
		if part.ToolName != session.TitleToolName || len(part.Input) == 0 {
			continue
		}
		input := struct {
			AITitle string `json:"aiTitle"`
		}{}
		if err := json.Unmarshal(part.Input, &input); err != nil {
			serviceLog.Warnf("decode %s input: %v", session.TitleToolName, err)
			continue
		}
		if strings.TrimSpace(input.AITitle) != "" {
			title = input.AITitle
		}
	}
	return title
}

// setThreadTitle applies a title, treating a losing or malformed one as
// nothing to do: naming a conversation must never fail its turn.
func (s *Service) setThreadTitle(ctx context.Context, threadID string, update TitleUpdate) {
	if threadID == "" || strings.TrimSpace(update.Title) == "" {
		return
	}
	store, err := s.threads(ctx)
	if err != nil {
		serviceLog.Warnf("title chat thread %q: %v", threadID, err)
		return
	}
	if err := store.SetTitle(ctx, threadID, update); err != nil {
		serviceLog.Warnf("title chat thread %q: %v", threadID, err)
	}
}

// sessionTitleTool lets the model name the conversation it is in. It is bound
// to one thread and injected per request, so it never appears in the catalog
// the user manages tool preferences against.
func (s *Service) sessionTitleTool(threadID string) api.ToolDefinition {
	readOnly := true
	return api.ToolDefinition{
		Name:        session.TitleToolName,
		Description: "Name the current conversation. " + sessionTitleInstruction,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				sessionTitleInput: map[string]any{
					"type":        "string",
					"description": "A short title for this conversation, at most eight words.",
				},
			},
			"required": []any{sessionTitleInput},
		},
		ReadOnlyHint:      &readOnly,
		DefaultPermission: api.ToolModeOn,
		Handler: func(ctx context.Context, input map[string]any) (any, error) {
			title, _ := input[sessionTitleInput].(string)
			if strings.TrimSpace(title) == "" {
				return nil, fmt.Errorf("%s requires a non-empty %s", session.TitleToolName, sessionTitleInput)
			}
			store, err := s.threads(ctx)
			if err != nil {
				return nil, err
			}
			if err := store.SetTitle(ctx, threadID, TitleUpdate{Title: title, Source: TitleSourceAI}); err != nil {
				return nil, err
			}
			return map[string]any{"title": title}, nil
		},
	}
}
