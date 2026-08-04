package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// workflowRendered builds a rendered CLI run carrying a workflow. The prompt
// body is empty so the run is verify-only: hooks execute with no model call.
func workflowRendered(verify *api.Verify) PromptRenderResult {
	model := api.Model{Name: "claude-sonnet-5", Backend: api.BackendClaudeCLI}
	return PromptRenderResult{
		Name:    "workflow-test",
		Model:   model.Name,
		Backend: string(model.Backend),
		Input: ai.Request{
			Model:    model,
			Workflow: &api.Workflow{Verify: verify},
		},
		Config: ai.Config{Model: model},
	}
}

type bufferedOnlyWorkflowProvider struct {
	executeCalls int
}

func (p *bufferedOnlyWorkflowProvider) GetModel() string       { return "buffered-model" }
func (p *bufferedOnlyWorkflowProvider) GetBackend() ai.Backend { return api.BackendDeepSeek }
func (p *bufferedOnlyWorkflowProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.executeCalls++
	return &ai.Response{
		Text:           "done",
		StructuredData: map[string]any{"status": "ok"},
		Model:          "buffered-model",
		Backend:        api.BackendDeepSeek,
		Usage:          ai.Usage{InputTokens: 2, OutputTokens: 3},
		CostUSD:        0.01,
	}, nil
}

type streamingWorkflowProvider struct {
	bufferedOnlyWorkflowProvider
	streamCalls int
}

func (p *streamingWorkflowProvider) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	p.streamCalls++
	events := make(chan ai.Event, 1)
	events <- ai.Event{Kind: ai.EventResult, Success: true}
	close(events)
	return events, nil
}

func TestWorkflowRunnerProviderHonorsNoStream(t *testing.T) {
	t.Run("buffered-only provider", func(t *testing.T) {
		provider := &bufferedOnlyWorkflowProvider{}
		runner, err := workflowRunnerProvider(provider, true, false)
		if err != nil {
			t.Fatal(err)
		}
		events, err := runner.ExecuteStream(context.Background(), ai.Request{})
		if err != nil {
			t.Fatal(err)
		}
		var got []ai.Event
		for event := range events {
			got = append(got, event)
		}
		if provider.executeCalls != 1 {
			t.Fatalf("Execute calls = %d, want 1", provider.executeCalls)
		}
		if len(got) != 2 || got[0].Kind != ai.EventText || got[0].Text != "done" || got[1].Kind != ai.EventResult {
			t.Fatalf("events = %+v, want final text and result", got)
		}
		if string(got[1].StructuredData) != `{"status":"ok"}` || got[1].Usage == nil || got[1].Usage.InputTokens != 2 || got[1].CostUSD != 0.01 {
			t.Fatalf("result event = %+v", got[1])
		}
	})

	t.Run("streaming remains unchanged", func(t *testing.T) {
		provider := &streamingWorkflowProvider{}
		runner, err := workflowRunnerProvider(provider, false, false)
		if err != nil {
			t.Fatal(err)
		}
		events, err := runner.ExecuteStream(context.Background(), ai.Request{})
		if err != nil {
			t.Fatal(err)
		}
		for range events {
		}
		if provider.streamCalls != 1 || provider.executeCalls != 0 {
			t.Fatalf("stream calls = %d, execute calls = %d", provider.streamCalls, provider.executeCalls)
		}
	})
}

// The CLI path must execute declared hooks, not skip them: a verify command
// that fails has to fail the run.
func TestExecuteSyncRun_CLIRunsVerifyHooks(t *testing.T) {
	isolateSavedAI(t)
	rendered := workflowRendered(&api.Verify{Commands: []string{"sh -c 'echo not done; exit 1'"}})
	rendered.Input.SetCwd(t.TempDir())

	_, err := executeSyncRunSingle(context.Background(), rendered, AIPromptOptions{})

	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("err = %v, want the failing verify command to fail the CLI run", err)
	}
}

func TestExecuteSyncRun_CLIVerifyHooksPass(t *testing.T) {
	isolateSavedAI(t)
	rendered := workflowRendered(&api.Verify{Commands: []string{"true"}})
	rendered.Input.SetCwd(t.TempDir())

	result, err := executeSyncRunSingle(context.Background(), rendered, AIPromptOptions{})

	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("status = %q", result.Status)
	}
}

// The reviewer's repro: a workflow naming a nonexistent judge prompt must fail
// the run loudly, not complete as if the check had passed.
func TestExecuteSyncRun_MissingJudgePromptFailsLoud(t *testing.T) {
	isolateSavedAI(t)
	rendered := workflowRendered(&api.Verify{Prompts: []string{"/does/not/exist.prompt"}})
	rendered.Input.SetCwd(t.TempDir())

	_, err := executeSyncRunSingle(context.Background(), rendered, AIPromptOptions{})

	if err == nil || !strings.Contains(err.Error(), "/does/not/exist.prompt") {
		t.Fatalf("err = %v, want a loud failure naming the missing prompt", err)
	}
}
