package cli

import (
	"context"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/gitagent"
)

func TestRunTaskPromptCarriesSupervisorRuntime(t *testing.T) {
	isolateSavedAI(t)
	original := executePromptRequestFunc
	t.Cleanup(func() { executePromptRequestFunc = original })

	var captured ai.Request
	executePromptRequestFunc = func(_ context.Context, req ai.Request, _ ai.Config, _ time.Duration, _ bool) (any, error) {
		captured = req
		return nil, nil
	}
	payload := gitagent.TaskPayload{
		Prompt: "make a change", Model: "gpt-5.6-sol",
		Backend: string(api.BackendCodexCLI), Effort: api.EffortHigh, Timeout: "17m",
	}
	if err := runTaskPrompt(context.Background(), t.TempDir(), payload); err != nil {
		t.Fatal(err)
	}
	if captured.Budget.Timeout != "17m" {
		t.Fatalf("timeout = %q, want 17m", captured.Budget.Timeout)
	}
	if captured.Effort != api.EffortHigh {
		t.Fatalf("effort = %q, want high", captured.Effort)
	}
}
