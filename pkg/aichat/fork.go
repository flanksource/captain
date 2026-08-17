package aichat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
)

const forkSeedPartType = "data-fork-seed"

type forkSeedMetadata struct {
	ForkedFrom string `json:"forkedFrom"`
	Title      string `json:"title,omitempty"`
}

func forkSeedMessage(source *Thread) (string, UIMessage, error) {
	hasConversation := false
	for _, message := range source.Messages {
		if !isForkSeedMessage(message) {
			hasConversation = true
			break
		}
	}
	if !hasConversation {
		return "", UIMessage{}, ErrForkSourceEmpty
	}
	metadata, err := json.Marshal(forkSeedMetadata{ForkedFrom: source.ID, Title: source.Title})
	if err != nil {
		return "", UIMessage{}, fmt.Errorf("encode fork provenance: %w", err)
	}
	label := strings.TrimSpace(source.Title)
	if label == "" {
		label = "Untitled"
	}
	return "Fork of " + label, UIMessage{
		ID: uuid.NewString() + "-fork-seed", Role: string(api.RoleUser),
		Parts: []UIPart{
			{Type: forkSeedPartType, Data: metadata},
			// Keep an earlier fork's flattened text when this is a fork-of-a-fork;
			// writeForkPart ignores its UI-only provenance marker.
			{Type: "text", Text: flattenForkTranscript(source.ID, source.Messages)},
		},
	}, nil
}

func isForkSeedMessage(message UIMessage) bool {
	for _, part := range message.Parts {
		if part.Type == forkSeedPartType {
			return true
		}
	}
	return false
}

func flattenForkTranscript(sourceID string, messages []UIMessage) string {
	var transcript strings.Builder
	fmt.Fprintf(&transcript, "<captain-fork source-session=%q>\n", sourceID)
	transcript.WriteString("The following is the conversation before it was forked. Continue from this context.\n\n")
	for _, message := range messages {
		role := strings.ToUpper(strings.TrimSpace(message.Role))
		if role == "" {
			role = "MESSAGE"
		}
		transcript.WriteString(role)
		transcript.WriteString(":\n")
		for _, part := range message.Parts {
			writeForkPart(&transcript, part)
		}
		transcript.WriteString("\n")
	}
	transcript.WriteString("</captain-fork>")
	return transcript.String()
}

func writeForkPart(transcript *strings.Builder, part UIPart) {
	switch {
	case part.Type == "text":
		transcript.WriteString(part.Text)
		transcript.WriteByte('\n')
	case part.Type == "file":
		fmt.Fprintf(transcript, "[Attachment: %s]\n", firstNonempty(part.Filename, part.MediaType, "file"))
	case part.IsTool():
		fmt.Fprintf(transcript, "[Tool %s", firstNonempty(part.EffectiveToolName(), "unknown"))
		if part.ToolCallID != "" {
			fmt.Fprintf(transcript, " (%s)", part.ToolCallID)
		}
		if len(part.Input) > 0 {
			fmt.Fprintf(transcript, " input=%s", part.Input)
		}
		if part.ErrorText != "" {
			fmt.Fprintf(transcript, " error=%s", part.ErrorText)
		} else if len(part.Output) > 0 {
			fmt.Fprintf(transcript, " output=%s", part.Output)
		}
		transcript.WriteString("]\n")
	}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
