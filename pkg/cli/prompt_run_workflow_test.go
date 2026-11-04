package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/sandbox/substraterun"
)

// workflowRendered builds a rendered CLI run carrying a workflow. The prompt
// body is empty so the run is verify-only: hooks execute with no model call.
func workflowRendered(verify *api.Verify) PromptRenderResult {
	model := api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeCLI}
	return PromptRenderResult{
		Name:     "workflow-test",
		Model:    model.Name,
		Provider: model.Provider.Name,
		Mode:     string(model.Mode),
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

// wholeRunSandbox stands in for a sandbox that runs whole runs: it answers the run it is handed.
type wholeRunSandbox struct{ prompts []string }

func (s *wholeRunSandbox) Kind() api.SandboxKind { return api.SandboxSubstrate }
func (s *wholeRunSandbox) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}
func (s *wholeRunSandbox) Close() error              { return nil }
func (s *wholeRunSandbox) IsolatesWorkspace() bool   { return true }
func (s *wholeRunSandbox) ProvidesEgressProxy() bool { return true }
func (s *wholeRunSandbox) RunRemote(_ context.Context, in promptrun.Input, started func(api.Spec) error) (promptrun.Result, error) {
	s.prompts = append(s.prompts, in.Resolved.Spec.Prompt.User)
	if err := started(in.Resolved.Spec); err != nil {
		return promptrun.Result{}, err
	}
	return promptrun.Result{Response: &api.Response{Text: "done in the environment"}, Passed: true}, nil
}

// A run on a sandbox that runs whole runs must be handed to it, not called directly here, where
// it would run unsandboxed.
func TestExecuteSyncRun_AWholeRunGoesToItsSandbox(t *testing.T) {
	isolateSavedAI(t)
	sandbox := &wholeRunSandbox{}
	api.RegisterSandbox(api.SandboxSubstrate, func(api.SandboxConfig) (api.Sandbox, error) { return sandbox, nil })
	t.Cleanup(func() { api.RegisterSandbox(api.SandboxSubstrate, substraterun.Substrate) })
	model := api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeCLI}
	rendered := PromptRenderResult{
		Name: "whole-run", Model: model.Name, Provider: model.Provider.Name, Mode: string(model.Mode),
		Input:  ai.Request{Model: model, Prompt: api.Prompt{User: "fix the build"}, Sandbox: &api.SandboxRef{Mode: api.SandboxSubstrate, Backend: "beta-substrate"}},
		Config: ai.Config{Model: model, SandboxSelection: &api.SandboxConfig{Kind: api.SandboxSubstrate, Name: "beta-substrate"}},
	}
	rendered.Input.SetCwd(t.TempDir())

	result, err := executeSyncRunSingle(context.Background(), rendered, AIPromptOptions{})

	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || len(sandbox.prompts) != 1 || sandbox.prompts[0] != "fix the build" {
		t.Fatalf("result = %+v, sandbox ran %v; want the run handed to the sandbox", result, sandbox.prompts)
	}
}
