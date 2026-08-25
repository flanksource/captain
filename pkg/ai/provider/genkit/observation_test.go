package genkit

import (
	"context"
	"testing"

	gkai "github.com/firebase/genkit/go/ai"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
)

func TestModelMiddlewareRecordsOpenAINativeReasoningEffort(t *testing.T) {
	recorder := observation.NewRecorder()
	ctx := observation.ContextWithRecorder(context.Background(), recorder)
	provider := &Provider{
		backend:  ai.BackendOpenAI,
		modelRef: "openai/gpt-5.6-sol",
		cfg: ai.Config{Model: api.Model{
			Name: "gpt-5.6-sol", Backend: api.BackendOpenAI,
		}},
	}
	hooks, err := provider.captureGenkitRequests(context.Background())
	if err != nil {
		t.Fatalf("create middleware: %v", err)
	}
	params := &gkai.ModelParams{Request: &gkai.ModelRequest{Config: map[string]any{"reasoning_effort": "high"}}}
	if _, err := hooks.WrapModel(ctx, params, func(context.Context, *gkai.ModelParams) (*gkai.ModelResponse, error) {
		return &gkai.ModelResponse{}, nil
	}); err != nil {
		t.Fatalf("invoke model middleware: %v", err)
	}
	snapshot := recorder.Snapshot()
	if len(snapshot.Dispatch) != 1 || snapshot.Dispatch[0].Boundary != "openai.chat.completions.create" {
		t.Fatalf("dispatch events = %#v", snapshot.Dispatch)
	}
	if snapshot.Effort.State != api.ObservationFactKnown || snapshot.Effort.Value == nil || *snapshot.Effort.Value != "high" {
		t.Fatalf("observed effort = %#v, want known high", snapshot.Effort)
	}
}
