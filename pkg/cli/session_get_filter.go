package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	historytools "github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/captain/pkg/session"
)

func filterSessionTranscript(s *session.Session, opts SessionGetOptions) error {
	if len(opts.Tools) == 0 && len(opts.Categories) == 0 {
		return nil
	}
	full := session.TranscriptWindow{
		Messages: len(s.Messages), Events: len(s.Events),
		ToolCalls: session.CountToolParts(s.Messages),
	}
	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())
	matchedCalls, err := matchingTranscriptToolCalls(s.Messages, opts, classifier)
	if err != nil {
		return err
	}
	s.Messages, err = filterTranscriptMessages(s.Messages, matchedCalls, opts, classifier)
	if err != nil {
		return err
	}
	s.Events = filterTranscriptEvents(s.Events, opts, classifier)
	if len(s.Messages) != full.Messages || len(s.Events) != full.Events ||
		session.CountToolParts(s.Messages) != full.ToolCalls {
		s.Window = &full
	}
	return nil
}

func matchingTranscriptToolCalls(messages []session.Message, opts SessionGetOptions, classifier *bash.CategoryClassifier) (map[string]struct{}, error) {
	matched := map[string]struct{}{}
	for _, message := range messages {
		for _, part := range message.Parts {
			use, ok, err := transcriptPartToolUse(message, part)
			if err != nil {
				return nil, err
			}
			if ok && part.ToolCallID != "" && transcriptToolUseMatches(use, opts, classifier) {
				matched[part.ToolCallID] = struct{}{}
			}
		}
	}
	return matched, nil
}

func filterTranscriptMessages(messages []session.Message, matchedCalls map[string]struct{}, opts SessionGetOptions, classifier *bash.CategoryClassifier) ([]session.Message, error) {
	filtered := make([]session.Message, 0, len(messages))
	for _, message := range messages {
		parts := make([]session.Part, 0, len(message.Parts))
		for _, part := range message.Parts {
			use, ok, err := transcriptPartToolUse(message, part)
			if err != nil {
				return nil, err
			}
			_, matchedResult := matchedCalls[part.ToolCallID]
			if (ok && transcriptToolUseMatches(use, opts, classifier)) || (!ok && matchedResult) {
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			message.Parts = parts
			message.Raw = nil
			filtered = append(filtered, message)
		}
	}
	return filtered, nil
}

func filterTranscriptEvents(events []session.Event, opts SessionGetOptions, classifier *bash.CategoryClassifier) []session.Event {
	filtered := make([]session.Event, 0, len(events))
	for _, event := range events {
		use := claude.ToolUse{Tool: historytools.EventToolName(event.Type), Input: event.Data}
		if transcriptToolUseMatches(use, opts, classifier) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func transcriptPartToolUse(message session.Message, part session.Part) (claude.ToolUse, bool, error) {
	use := claude.ToolUse{}
	switch part.Type {
	case session.PartText:
		use.Tool, use.Input = transcriptRoleTool(message.Role), map[string]any{"text": part.Text}
	case session.PartReasoning:
		use.Tool, use.Input = "Reasoning", map[string]any{"text": part.Text}
	case session.PartFile:
		use.Tool = "File"
		use.Input = map[string]any{"filename": part.Filename, "url": part.URL, "mediaType": part.MediaType}
	default:
		use.Tool = strings.TrimSpace(part.ToolName)
		if use.Tool == "" && strings.HasPrefix(part.Type, "tool-") {
			use.Tool = strings.TrimPrefix(part.Type, "tool-")
		}
		if use.Tool == "" {
			return claude.ToolUse{}, false, nil
		}
		if len(part.Input) > 0 {
			if err := json.Unmarshal(part.Input, &use.Input); err != nil {
				return claude.ToolUse{}, false, fmt.Errorf("decode %s tool input: %w", use.Tool, err)
			}
		}
	}
	if message.Provenance != nil {
		use.CWD = message.Provenance.CWD
		use.Source = message.Provenance.Source
		use.SessionID = message.Provenance.SessionID
	}
	return use, true, nil
}

func transcriptToolUseMatches(use claude.ToolUse, opts SessionGetOptions, classifier *bash.CategoryClassifier) bool {
	if len(opts.Tools) > 0 && !toolFiltersMayInclude(opts.Tools, use.Tool) {
		return false
	}
	tool := claude.ToolUsesToTools([]claude.ToolUse{use})[0]
	category := classifyTool(tool, classifier)
	return matchCategoryFilters(categoryFilterCandidates(tool, category), opts.Categories)
}

func transcriptRoleTool(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	default:
		if role == "" {
			return "Message"
		}
		return strings.ToUpper(role[:1]) + role[1:]
	}
}
