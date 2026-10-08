package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

const (
	streamSession = "sess-stream"
	streamCostUSD = 0.25
)

// streamingProvider streams one scripted structured result per call (repeating
// the last), opening every turn with the session it runs in, and records each
// request so a repair turn's resume and feedback can be asserted.
type streamingProvider struct {
	results  []string
	requests []ai.Request
}

func (s *streamingProvider) GetModel() string       { return "test-model" }
func (s *streamingProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeCLI) }
func (s *streamingProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return nil, errors.New("streamingProvider: Execute not scripted")
}
func (s *streamingProvider) ExecuteStream(_ context.Context, req ai.Request) (<-chan ai.Event, error) {
	idx := min(len(s.requests), len(s.results)-1)
	s.requests = append(s.requests, req)
	ch := make(chan ai.Event, 4)
	ch <- ai.Event{Kind: ai.EventSystem, SessionID: streamSession}
	ch <- ai.Event{Kind: ai.EventResult, Success: true, CostUSD: streamCostUSD,
		Usage: &ai.Usage{OutputTokens: 10}, StructuredData: json.RawMessage(s.results[idx])}
	close(ch)
	return ch, nil
}

func streamWith(t *testing.T, inner *streamingProvider) []ai.Event {
	t.Helper()
	p, err := WithSchemaValidation()(inner)
	if err != nil {
		t.Fatalf("WithSchemaValidation: %v", err)
	}
	events, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), capRequest(api.SchemaStrictnessRetry))
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var out []ai.Event
	for ev := range events {
		out = append(out, ev)
	}
	return out
}

func eventsOfKind(events []ai.Event, kind ai.EventKind) []ai.Event {
	var out []ai.Event
	for _, ev := range events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func TestValidationStream_RetryResumesTheSessionWithTheErrors(t *testing.T) {
	inner := &streamingProvider{results: []string{overCapJSON, validJSON}}
	events := streamWith(t, inner)

	if errs := eventsOfKind(events, ai.EventError); len(errs) != 0 {
		t.Fatalf("a repaired stream must not fail, got %v", errs)
	}
	results := eventsOfKind(events, ai.EventResult)
	if len(results) != 1 {
		t.Fatalf("want exactly one terminal result, got %d", len(results))
	}
	if string(results[0].StructuredData) != validJSON {
		t.Errorf("want the repaired result %s, got %s", validJSON, results[0].StructuredData)
	}
	if results[0].CostUSD != 2*streamCostUSD || results[0].Usage.OutputTokens != 20 {
		t.Errorf("the result must carry both turns' cost and usage, got $%v / %+v", results[0].CostUSD, results[0].Usage)
	}
	if len(inner.requests) != 2 {
		t.Fatalf("want 1 turn + 1 repair, got %d", len(inner.requests))
	}
	repair := inner.requests[1]
	if repair.SessionID != streamSession {
		t.Errorf("the repair must resume %q, got %q", streamSession, repair.SessionID)
	}
	if !strings.Contains(repair.Prompt.User, "groups") || !strings.Contains(repair.Prompt.User, overCapJSON) {
		t.Errorf("the repair prompt must carry the errors and the previous response, got %q", repair.Prompt.User)
	}
}

func TestValidationStream_RetryFailsAfterTheBudget(t *testing.T) {
	inner := &streamingProvider{results: []string{overCapJSON}}
	events := streamWith(t, inner)

	errs := eventsOfKind(events, ai.EventError)
	if len(errs) != 1 || !strings.Contains(errs[0].Error, ai.ErrSchemaValidation.Error()) {
		t.Fatalf("want one schema validation error, got %v", errs)
	}
	if want := 1 + maxSchemaRetries; len(inner.requests) != want {
		t.Errorf("want %d turns, got %d", want, len(inner.requests))
	}
}
