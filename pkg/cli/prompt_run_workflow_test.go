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
