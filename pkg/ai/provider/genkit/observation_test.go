package genkit

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	gkai "github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
)

type countingOpenAIDoer struct {
	calls  int
	effort string
}

func (d *countingOpenAIDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	var payload struct {
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		return nil, err
	}
	d.effort = payload.ReasoningEffort
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"completion-1","object":"chat.completion","created":0,"model":"gpt-5.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		)),
		Request: req,
	}, nil
}

func TestOpenAIConversionFailureDoesNotRecordNativeDispatch(t *testing.T) {
	recorder := observation.NewRecorder()
	ctx := observation.ContextWithRecorder(context.Background(), recorder)
	doer := &countingOpenAIDoer{}
	client := openaisdk.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL("https://openai.example.test/v1"),
		option.WithHTTPClient(doer),
		option.WithMaxRetries(0),
		option.WithMiddleware(observeOpenAIReasoningDispatch),
	)
	generator := compat_oai.NewModelGenerator(&client, "gpt-5.6")
	generator.WithConfig(map[string]any{"reasoning_effort": "high", "temperature": math.Inf(1)})
	generator.WithMessages([]*gkai.Message{gkai.NewUserTextMessage("hello")})

	_, err := generator.Generate(ctx, &gkai.ModelRequest{}, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to convert config") {
		t.Fatalf("Generate error = %v, want config conversion failure", err)
	}
	if doer.calls != 0 {
		t.Fatalf("provider transport calls = %d, want 0", doer.calls)
	}
	snapshot := recorder.Snapshot()
	if len(snapshot.Dispatch) != 0 {
		t.Fatalf("dispatch events = %#v, want none", snapshot.Dispatch)
	}
	if snapshot.Effort.State != api.ObservationFactUnknown || snapshot.Effort.ReasonCode != "dispatch_not_observed" {
		t.Fatalf("observed effort = %#v, want unknown dispatch_not_observed", snapshot.Effort)
	}
}

func TestOpenAINativeDispatchRecordsMarshaledReasoningEffort(t *testing.T) {
	recorder := observation.NewRecorder()
	ctx := observation.ContextWithRecorder(context.Background(), recorder)
	doer := &countingOpenAIDoer{}
	client := openaisdk.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL("https://openai.example.test/v1"),
		option.WithHTTPClient(doer),
		option.WithMaxRetries(0),
		option.WithMiddleware(observeOpenAIReasoningDispatch),
	)
	generator := compat_oai.NewModelGenerator(&client, "gpt-5.6")
	generator.WithConfig(map[string]any{"reasoning_effort": "high"})
	generator.WithMessages([]*gkai.Message{gkai.NewUserTextMessage("hello")})

	if _, err := generator.Generate(ctx, &gkai.ModelRequest{}, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if doer.calls != 1 || doer.effort != "high" {
		t.Fatalf("provider transport calls = %d, reasoning_effort = %q; want 1, high", doer.calls, doer.effort)
	}
	snapshot := recorder.Snapshot()
	if len(snapshot.Dispatch) != 1 || snapshot.Dispatch[0].Boundary != openAIChatCompletionsBoundary {
		t.Fatalf("dispatch events = %#v", snapshot.Dispatch)
	}
	if snapshot.Effort.State != api.ObservationFactKnown || snapshot.Effort.Value == nil || *snapshot.Effort.Value != "high" {
		t.Fatalf("observed effort = %#v, want known high", snapshot.Effort)
	}
}
