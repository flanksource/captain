package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// outcome is one scripted provider result: a text response or an error.
type outcome struct {
	text string
	err  error
}

// erroringProvider scripts a sequence of (response|error) outcomes (repeating the
// last), records every request's user prompt, and counts calls — so a provider-side
// schema rejection and the feedback appended on the retry can both be asserted.
type erroringProvider struct {
	outcomes []outcome
	calls    int
	prompts  []string
}

func (e *erroringProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	e.prompts = append(e.prompts, req.Prompt.User)
	idx := e.calls
	if idx >= len(e.outcomes) {
		idx = len(e.outcomes) - 1
	}
	e.calls++
	if o := e.outcomes[idx]; o.err != nil {
		return nil, o.err
	}
	return &ai.Response{Text: e.outcomes[idx].text, Model: "test-model"}, nil
}
func (e *erroringProvider) GetModel() string       { return "test-model" }
func (e *erroringProvider) GetBackend() ai.Backend { return ai.BackendAnthropic }

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
	// Every attempt violates → hard error after the retry budget is exhausted
	// (1 initial call + maxSchemaRetries re-asks).
	_, err, calls := execWith(t, api.SchemaStrictnessRetry, overCapJSON)
	if !errors.Is(err, ai.ErrSchemaValidation) {
		t.Fatalf("want ErrSchemaValidation after retries, got %v", err)
	}
	if want := 1 + maxSchemaRetries; calls != want {
		t.Errorf("retry must re-ask maxSchemaRetries times; want %d calls, got %d", want, calls)
	}
}

func TestValidation_RetryRecoversProviderSchemaError(t *testing.T) {
	// A genkit-style hard rejection during generation on the first call, then a
	// conforming response on the retry: the middleware re-asks and returns the
	// valid response instead of surfacing the error.
	schemaErr := fmt.Errorf("%w: - groups: Array must have at most 2 items", ai.ErrSchemaValidation)
	inner := &erroringProvider{outcomes: []outcome{{err: schemaErr}, {text: validJSON}}}
	p, err := WithSchemaValidation()(inner)
	if err != nil {
		t.Fatalf("WithSchemaValidation: %v", err)
	}
	resp, err := p.Execute(context.Background(), capRequest(api.SchemaStrictnessRetry))
	if err != nil {
		t.Fatalf("provider schema error should recover on retry, got %v", err)
	}
	if resp.Text != validJSON {
		t.Errorf("want the corrected response, got %q", resp.Text)
	}
	if inner.calls != 2 {
		t.Errorf("want 1 initial + 1 retry = 2 calls, got %d", inner.calls)
	}
	// The re-ask must feed the schema errors back so the model can correct itself.
	if len(inner.prompts) < 2 || !strings.Contains(inner.prompts[1], "Array must have at most 2 items") {
		t.Errorf("retry prompt must include the validation errors, got %v", inner.prompts)
	}
}

func TestValidation_ProviderSchemaErrorPassesThroughWithoutRetryStrictness(t *testing.T) {
	// Without retry strictness a provider schema error surfaces as-is; no re-ask.
	schemaErr := fmt.Errorf("%w: bad", ai.ErrSchemaValidation)
	inner := &erroringProvider{outcomes: []outcome{{err: schemaErr}}}
	p, err := WithSchemaValidation()(inner)
	if err != nil {
		t.Fatalf("WithSchemaValidation: %v", err)
	}
	_, err = p.Execute(context.Background(), capRequest(api.SchemaStrictnessNone))
	if !errors.Is(err, ai.ErrSchemaValidation) {
		t.Fatalf("want the provider error surfaced, got %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("none strictness must not retry; want 1 call, got %d", inner.calls)
	}
}

func TestValidation_ProviderSchemaErrorExhaustsRetries(t *testing.T) {
	// A provider that keeps rejecting during generation exhausts the retry budget
	// and fails hard with ErrSchemaValidation.
	schemaErr := fmt.Errorf("%w: - groups: too many", ai.ErrSchemaValidation)
	inner := &erroringProvider{outcomes: []outcome{{err: schemaErr}}} // repeats
	p, err := WithSchemaValidation()(inner)
	if err != nil {
		t.Fatalf("WithSchemaValidation: %v", err)
	}
	_, err = p.Execute(context.Background(), capRequest(api.SchemaStrictnessRetry))
	if !errors.Is(err, ai.ErrSchemaValidation) {
		t.Fatalf("want ErrSchemaValidation after exhausting retries, got %v", err)
	}
	if want := 1 + maxSchemaRetries; inner.calls != want {
		t.Errorf("want %d calls, got %d", want, inner.calls)
	}
}
