package genkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"

	gkai "github.com/firebase/genkit/go/ai"
)

type toolEventCorrelation struct {
	mu       sync.Mutex
	pending  []*gkai.ToolRequest
	started  map[string]string
	finished map[string]bool
}

func newToolEventCorrelation() *toolEventCorrelation {
	return &toolEventCorrelation{
		started:  make(map[string]string),
		finished: make(map[string]bool),
	}
}

// chunkToEvents translates text and reasoning while retaining provider tool
// requests for the in-process handler. Tool lifecycle events are emitted only
// by runTool, after the handler can correlate the call to its provider ref.
func chunkToEvents(chunk *gkai.ModelResponseChunk, model string, correlation *toolEventCorrelation) ([]ai.Event, error) {
	if chunk == nil {
		return nil, nil
	}
	var events []ai.Event
	for _, p := range chunk.Content {
		switch {
		case p.IsText() && p.Text != "":
			events = append(events, ai.Event{Kind: ai.EventText, Text: p.Text, Model: model})
		case p.IsReasoning() && p.Text != "":
			events = append(events, ai.Event{Kind: ai.EventThinking, Text: p.Text, Model: model})
		case p.IsToolRequest() && p.ToolRequest != nil:
			if correlation != nil {
				correlation.observeRequest(p.ToolRequest)
			}
		case p.IsToolResponse() && p.ToolResponse != nil && !p.IsPartial():
			if correlation == nil {
				return nil, fmt.Errorf("genkit tool response %q has no correlation state", p.ToolResponse.Ref)
			}
			if err := correlation.observeResponse(p.ToolResponse); err != nil {
				return nil, err
			}
		}
	}
	return events, nil
}

func (c *toolEventCorrelation) observeRequest(request *gkai.ToolRequest) {
	if request == nil || request.Partial || request.Name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, pending := range c.pending {
		if pending == request || (request.Ref != "" && pending.Ref == request.Ref) {
			return
		}
	}
	if request.Ref != "" {
		if _, ok := c.started[request.Ref]; ok {
			return
		}
	}
	c.pending = append(c.pending, request)
}

func (c *toolEventCorrelation) begin(request *gkai.ToolRequest) (string, error) {
	if request == nil || request.Name == "" {
		return "", fmt.Errorf("genkit tool execution has no provider request name")
	}
	if request.Ref == "" {
		return "", fmt.Errorf("genkit tool %q provider request has no call reference", request.Name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, pending := range c.pending {
		if pending.Ref != request.Ref {
			continue
		}
		if pending.Name != request.Name {
			return "", fmt.Errorf("genkit tool request %q names %q, expected %q", request.Ref, request.Name, pending.Name)
		}
		if _, exists := c.started[request.Ref]; exists {
			return "", fmt.Errorf("genkit tool request %q started more than once", request.Ref)
		}
		c.pending = append(c.pending[:i], c.pending[i+1:]...)
		c.started[request.Ref] = request.Name
		return request.Ref, nil
	}
	return "", fmt.Errorf("genkit tool %q execution reference %q has no correlated provider request", request.Name, request.Ref)
}

func (c *toolEventCorrelation) finish(ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.started[ref]; !ok {
		return fmt.Errorf("genkit tool result %q has no correlated provider request", ref)
	}
	if c.finished[ref] {
		return fmt.Errorf("genkit tool result %q emitted more than once", ref)
	}
	c.finished[ref] = true
	return nil
}

func (c *toolEventCorrelation) seedResolved(request *gkai.ToolRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started[request.Ref] = request.Name
	c.finished[request.Ref] = true
}

func (c *toolEventCorrelation) observeResponse(response *gkai.ToolResponse) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if response.Ref == "" {
		return fmt.Errorf("genkit tool response has no provider call reference")
	}
	name, ok := c.started[response.Ref]
	if !ok {
		return fmt.Errorf("genkit received uncorrelated tool response %q", response.Ref)
	}
	if name != response.Name {
		return fmt.Errorf("genkit tool response %q names %q, expected %q", response.Ref, response.Name, name)
	}
	if !c.finished[response.Ref] {
		return fmt.Errorf("genkit tool response %q arrived before its result", response.Ref)
	}
	delete(c.started, response.Ref)
	delete(c.finished, response.Ref)
	return nil
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
// generation and records native usage presence before flattening it to a value.
// Structured output and cost are applied by the caller.
func responseToResponse(ctx context.Context, resp *gkai.ModelResponse, backend ai.Backend, model string, start time.Time) *ai.Response {
	out := &ai.Response{
		Text:     resp.Text(),
		Model:    model,
		Backend:  backend,
		Usage:    mapUsage(resp.Usage, backend),
		Duration: time.Since(start),
		Raw:      resp,
	}
	var usage *ai.Usage
	if resp.Usage != nil {
		usage = &out.Usage
	}
	observation.RecordUsage(ctx, usage)
	return out
}
