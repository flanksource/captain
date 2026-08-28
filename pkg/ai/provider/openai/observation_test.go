package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
)

func TestPreparationFailureDoesNotRecordResponsesDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	provider, err := New(ai.Config{
		Model:  api.Model{Name: "gpt-5", Backend: api.BackendOpenAI},
		APIKey: "test-key", APIURL: server.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := observation.NewRecorder()
	ctx := observation.ContextWithRecorder(context.Background(), recorder)
	_, err = provider.ExecuteStream(ctx, ai.Request{
		Model:  api.Model{Effort: api.EffortHigh},
		Prompt: api.Prompt{User: "hello", SchemaJSON: json.RawMessage(`{"type":`)},
	})
	if err == nil {
		t.Fatal("ExecuteStream error = nil, want schema preparation failure")
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls.Load())
	}
	snapshot := recorder.Snapshot()
	if len(snapshot.Dispatch) != 0 || snapshot.Effort.State != api.ObservationFactUnknown {
		t.Fatalf("snapshot = %#v, want no dispatch evidence", snapshot)
	}
}

func TestResponsesDispatchRecordsNativeParams(t *testing.T) {
	server, body := newObservationResponsesServer(t, zeroUsageJSON, "ok")
	defer server.Close()
	provider, err := New(ai.Config{
		Model:  api.Model{Name: "gpt-5", Backend: api.BackendOpenAI},
		APIKey: "test-key", APIURL: server.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := observation.NewRecorder()
	ctx := observation.ContextWithRecorder(context.Background(), recorder)
	events, err := provider.ExecuteStream(ctx, ai.Request{
		Model: api.Model{Effort: api.EffortHigh}, Prompt: api.Prompt{User: "hello"},
	})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	for range events {
	}

	var request struct {
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(*body, &request); err != nil {
		t.Fatalf("decode SDK request: %v", err)
	}
	if request.Reasoning.Effort != "high" {
		t.Fatalf("SDK request effort = %q, want high", request.Reasoning.Effort)
	}
	snapshot := recorder.Snapshot()
	if len(snapshot.Dispatch) != 1 || snapshot.Dispatch[0].Boundary != responsesDispatchBoundary {
		t.Fatalf("dispatch = %#v", snapshot.Dispatch)
	}
	if snapshot.Effort.State != api.ObservationFactKnown || snapshot.Effort.Value == nil || *snapshot.Effort.Value != request.Reasoning.Effort {
		t.Fatalf("observed effort = %#v, SDK request = %#v", snapshot.Effort, request.Reasoning)
	}
}

func TestResponsesUsagePresenceSurvivesStreamingAndBufferedPaths(t *testing.T) {
	tests := []struct {
		name      string
		usage     string
		buffered  bool
		wantKnown bool
	}{
		{name: "streaming omitted", usage: "", wantKnown: false},
		{name: "streaming present zero", usage: zeroUsageJSON, wantKnown: true},
		{name: "buffered structured omitted", usage: "", buffered: true, wantKnown: false},
		{name: "buffered structured present zero", usage: zeroUsageJSON, buffered: true, wantKnown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newObservationResponsesServer(t, test.usage, `{"ok":true}`)
			defer server.Close()
			provider, err := New(ai.Config{
				Model:  api.Model{Name: "gpt-5", Backend: api.BackendOpenAI},
				APIKey: "test-key", APIURL: server.URL,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			recorder := observation.NewRecorder()
			ctx := observation.ContextWithRecorder(context.Background(), recorder)
			req := ai.Request{Prompt: api.Prompt{User: "hello"}}
			var compatibilityUsage ai.Usage
			compatibilityUsagePresent := test.buffered
			if test.buffered {
				req.Prompt.SchemaJSON = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)
				response, err := provider.Execute(ctx, req)
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				compatibilityUsage = response.Usage
			} else {
				events, err := provider.ExecuteStream(ctx, req)
				if err != nil {
					t.Fatalf("ExecuteStream: %v", err)
				}
				for event := range events {
					if event.Kind == ai.EventResult && event.Usage != nil {
						compatibilityUsage = *event.Usage
						compatibilityUsagePresent = true
					}
				}
			}
			if !compatibilityUsagePresent || compatibilityUsage != (ai.Usage{}) {
				t.Fatalf("ordinary provider usage = %#v (present %v), want compatibility zero value", compatibilityUsage, compatibilityUsagePresent)
			}
			usage := recorder.Snapshot().Usage
			if test.wantKnown && (usage == nil || *usage != (api.Usage{})) {
				t.Fatalf("usage = %#v, want present all-zero", usage)
			}
			if !test.wantKnown && usage != nil {
				t.Fatalf("usage = %#v, want absent", usage)
			}
		})
	}
}

const zeroUsageJSON = `{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}`

func newObservationResponsesServer(t *testing.T, usage, output string) (*httptest.Server, *[]byte) {
	t.Helper()
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var err error
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		usageField := ""
		if usage != "" {
			usageField = `,"usage":` + usage
		}
		response := fmt.Sprintf(`{"id":"resp-1","object":"response","status":"completed","model":"gpt-5","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}],"error":null,"created_at":0%s}`, output, usageField)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
	}))
	return server, &requestBody
}

func TestResponsesDispatchRecordsNativeOmission(t *testing.T) {
	server, body := newObservationResponsesServer(t, zeroUsageJSON, "ok")
	defer server.Close()
	provider, err := New(ai.Config{
		Model:  api.Model{Name: "gpt-5", Backend: api.BackendOpenAI},
		APIKey: "test-key", APIURL: server.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := observation.NewRecorder()
	events, err := provider.ExecuteStream(observation.ContextWithRecorder(context.Background(), recorder), ai.Request{
		Prompt: api.Prompt{User: "hello"},
	})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	for range events {
	}
	if strings.Contains(string(*body), `"effort"`) {
		t.Fatalf("SDK request unexpectedly contains effort: %s", *body)
	}
	snapshot := recorder.Snapshot()
	if snapshot.Effort.State != api.ObservationFactUnset || len(snapshot.Effort.EvidenceRefs) != 1 {
		t.Fatalf("observed effort = %#v, want evidenced unset", snapshot.Effort)
	}
}
