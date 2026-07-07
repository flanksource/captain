package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// capSchema requires an object with a groups array of at most two items — the
// minimal shape that exercises a maxItems violation.
const capSchema = `{"type":"object","required":["groups"],"properties":{"groups":{"type":"array","maxItems":2}}}`

const (
	overCapJSON = `{"groups":[{},{},{}]}` // 3 items — violates maxItems:2
	validJSON   = `{"groups":[{},{}]}`    // 2 items — conforms
)

// scriptedProvider returns its responses in order (repeating the last), counting
// calls so a retry can be observed.
type scriptedProvider struct {
	responses []string
	calls     int
}

func (s *scriptedProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	idx := s.calls
	if idx >= len(s.responses) {
		idx = len(s.responses) - 1
	}
	s.calls++
	return &ai.Response{Text: s.responses[idx], Model: "test-model"}, nil
}
func (s *scriptedProvider) GetModel() string       { return "test-model" }
func (s *scriptedProvider) GetBackend() ai.Backend { return ai.BackendAnthropic }

func capRequest(strictness api.SchemaStrictness) ai.Request {
	return ai.Request{Prompt: api.Prompt{
		User:             "group the files",
		SchemaJSON:       json.RawMessage(capSchema),
		SchemaStrictness: strictness,
	}}
}

func execWith(t *testing.T, strictness api.SchemaStrictness, responses ...string) (*ai.Response, error, int) {
	t.Helper()
	inner := &scriptedProvider{responses: responses}
	p, err := WithSchemaValidation()(inner)
	if err != nil {
		t.Fatalf("WithSchemaValidation: %v", err)
	}
	resp, err := p.Execute(context.Background(), capRequest(strictness))
	return resp, err, inner.calls
}

func TestValidation_NoStrictnessSkipsValidation(t *testing.T) {
	// "" disables validation: an over-cap response passes through unchanged, one call.
	resp, err, calls := execWith(t, api.SchemaStrictnessNone, overCapJSON)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if resp.Text != overCapJSON {
		t.Errorf("want passthrough response, got %q", resp.Text)
	}
	if calls != 1 {
		t.Errorf("want 1 provider call, got %d", calls)
	}
}

func TestValidation_WarningLogsAndReturns(t *testing.T) {
	resp, err, calls := execWith(t, api.SchemaStrictnessWarning, overCapJSON)
	if err != nil {
		t.Fatalf("warning mode must not error, got %v", err)
	}
	if resp.Text != overCapJSON || calls != 1 {
		t.Errorf("warning mode should return the response as-is in one call; got calls=%d resp=%q", calls, resp.Text)
	}
}

func TestValidation_ErrorFailsOnViolation(t *testing.T) {
	_, err, calls := execWith(t, api.SchemaStrictnessError, overCapJSON)
	if !errors.Is(err, ai.ErrSchemaValidation) {
		t.Fatalf("want ErrSchemaValidation, got %v", err)
	}
	if calls != 1 {
		t.Errorf("error mode must not retry; want 1 call, got %d", calls)
	}
}

func TestValidation_ErrorPassesWhenValid(t *testing.T) {
	resp, err, calls := execWith(t, api.SchemaStrictnessError, validJSON)
	if err != nil {
		t.Fatalf("a conforming response must not error, got %v", err)
	}
	if resp.Text != validJSON || calls != 1 {
		t.Errorf("want the conforming response in one call; got calls=%d", calls)
	}
}

func TestValidation_RetrySucceedsOnSecond(t *testing.T) {
	// First response violates; the single retry conforms → returned, exactly 2 calls.
	resp, err, calls := execWith(t, api.SchemaStrictnessRetry, overCapJSON, validJSON)
	if err != nil {
		t.Fatalf("retry that conforms must not error, got %v", err)
	}
	if resp.Text != validJSON {
		t.Errorf("want the corrected response, got %q", resp.Text)
	}
	if calls != 2 {
		t.Errorf("retry must re-ask exactly once; want 2 calls, got %d", calls)
	}
}

func TestValidation_RetryThenErrorWhenStillInvalid(t *testing.T) {
	// Both attempts violate → hard error after exactly one retry (retry → then error).
	_, err, calls := execWith(t, api.SchemaStrictnessRetry, overCapJSON, overCapJSON)
	if !errors.Is(err, ai.ErrSchemaValidation) {
		t.Fatalf("want ErrSchemaValidation after retry, got %v", err)
	}
	if calls != 2 {
		t.Errorf("retry must re-ask exactly once; want 2 calls, got %d", calls)
	}
}
