package genkit

import (
	"encoding/json"
	"time"

	"github.com/flanksource/captain/pkg/ai"

	gkai "github.com/firebase/genkit/go/ai"
)

// chunkToEvents translates a streamed genkit chunk into captain stream events:
// text parts -> EventText, reasoning parts -> EventThinking, and completed
// (non-partial) tool requests -> EventToolUse.
func chunkToEvents(chunk *gkai.ModelResponseChunk, model string) []ai.Event {
	if chunk == nil {
		return nil
	}
	var events []ai.Event
	for _, p := range chunk.Content {
		switch {
		case p.IsText() && p.Text != "":
			events = append(events, ai.Event{Kind: ai.EventText, Text: p.Text, Model: model})
		case p.IsReasoning() && p.Text != "":
			events = append(events, ai.Event{Kind: ai.EventThinking, Text: p.Text, Model: model})
		case p.IsToolRequest() && p.ToolRequest != nil && !p.ToolRequest.Partial:
			tr := p.ToolRequest
			events = append(events, ai.Event{
				Kind:  ai.EventToolUse,
				Tool:  tr.Name,
				Input: toInputMap(tr.Input),
				Model: model,
			})
		}
	}
	return events
}

// toInputMap normalizes a genkit tool-request input (typed any) into the
// map[string]any shape captain's Event.Input expects.
func toInputMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// mapUsage maps genkit's GenerationUsage onto captain's disjoint-bucket Usage.
// genkit folds cache reads into InputTokens for Gemini and the OpenAI-compatible
// backends (OpenAI/DeepSeek), and folds reasoning into OutputTokens for the
// OpenAI-compatible backends; Anthropic reports InputTokens already net of cache,
// and both Anthropic and Gemini report OutputTokens without reasoning. Normalize
// to the disjoint contract so the pricing registry and TotalTokens do not
// double-count. genkit's GenerationUsage has no cache-write field, so
// CacheWriteTokens is always zero here — cache-write spend on the API path is
// invisible upstream (finding C4).
func mapUsage(u *gkai.GenerationUsage, backend ai.Backend) ai.Usage {
	if u == nil {
		return ai.Usage{}
	}
	input := u.InputTokens
	if backend != ai.BackendAnthropic {
		input = ai.NetInputTokens(u.InputTokens, u.CachedContentTokens)
	}
	output := u.OutputTokens
	if backend == ai.BackendOpenAI || backend == ai.BackendDeepSeek {
		output = ai.NetOutputTokens(u.OutputTokens, u.ThoughtsTokens)
	}
	return ai.Usage{
		InputTokens:     input,
		OutputTokens:    output,
		ReasoningTokens: u.ThoughtsTokens,
		CacheReadTokens: u.CachedContentTokens,
	}
}

// responseToResponse builds the base captain response from a buffered genkit
// generation. Structured output and cost are applied by the caller.
func responseToResponse(resp *gkai.ModelResponse, backend ai.Backend, model string, start time.Time) *ai.Response {
	return &ai.Response{
		Text:     resp.Text(),
		Model:    model,
		Backend:  backend,
		Usage:    mapUsage(resp.Usage, backend),
		Duration: time.Since(start),
		Raw:      resp,
	}
}
