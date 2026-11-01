package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
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
	var capturedConfig ai.Config
	executePromptRequestFunc = func(_ context.Context, req ai.Request, cfg ai.Config, _ time.Duration, _ bool) (any, error) {
		captured, capturedConfig = req, cfg
		return nil, nil
	}
	payload := gitagent.TaskPayload{
		Prompt: "make a change", Model: "gpt-5.6-sol",
		Provider: api.OpenAI.Name, Mode: api.ModeCLI, Effort: api.EffortHigh, Timeout: "17m",
	}
	tools := &api.CallerToolEndpoint{Name: "captain", URL: "http://127.0.0.1:4711/mcp", Headers: map[string]string{"Authorization": "Bearer cap_x.y"}}
	if err := runTaskPrompt(context.Background(), t.TempDir(), payload, taskToolset{endpoint: tools}); err != nil {
		t.Fatal(err)
	}
	if captured.Budget.Timeout != "17m" {
		t.Fatalf("timeout = %q, want 17m", captured.Budget.Timeout)
	}
	if captured.Effort != api.EffortHigh {
		t.Fatalf("effort = %q, want high", captured.Effort)
	}
	if capturedConfig.CallerTools != tools {
		t.Fatalf("caller tools = %+v, want the forwarded supervisor endpoint", capturedConfig.CallerTools)
	}
}

// A task whose tools hold credentials here keeps the agent from reading what
// grants them, through a policy the agent's own native sandbox enforces.
func TestRunTaskPromptConfinesTheAgentBesideCredentialedTools(t *testing.T) {
	isolateSavedAI(t)
	original := executePromptRequestFunc
	t.Cleanup(func() { executePromptRequestFunc = original })
	var captured ai.Request
	executePromptRequestFunc = func(_ context.Context, req ai.Request, _ ai.Config, _ time.Duration, _ bool) (any, error) {
		captured = req
		return nil, nil
	}
	payload := gitagent.TaskPayload{Prompt: "use the tools", Model: "claude-sonnet-5", Provider: api.Anthropic.Name, Mode: api.ModeCLI}
	denied := []string{"/srv/repo.git/captain/tasks/t-1/task.json", "/srv/keys/agent_ed25519"}
	if err := runTaskPrompt(context.Background(), t.TempDir(), payload, taskToolset{denyRead: denied}); err != nil {
		t.Fatal(err)
	}
	if captured.Sandbox == nil || captured.Sandbox.Policy == nil || captured.Sandbox.Policy.Filesystem == nil ||
		!slices.Equal(captured.Sandbox.Policy.Filesystem.DeniedReadRoots, denied) {
		t.Fatalf("sandbox = %+v, want native with deniedReadRoots %v", captured.Sandbox, denied)
	}
	translated, err := api.TranslateClaudeSandbox(api.RuntimeOf(api.Anthropic, api.ModeCLI), *captured.Sandbox)
	if err != nil {
		t.Fatalf("claude's native sandbox refused the confinement: %v", err)
	}
	if !strings.Contains(fmt.Sprint(translated), denied[1]) {
		t.Fatalf("claude settings %v do not deny %s", translated, denied[1])
	}
}

func TestAgentBesideCredentialedToolsKeepsOnlyItsOwnEnvironment(t *testing.T) {
	for name, keep := range map[string]bool{
		"PATH": true, "HOME": true, "HTTPS_PROXY": true, "CAPTAIN_TASK": true,
		"ANTHROPIC_API_KEY": true, "OPENAI_API_KEY": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
		"AWS_SECRET_ACCESS_KEY": false, "AWS_WEB_IDENTITY_TOKEN_FILE": false, "GITHUB_TOKEN": false,
		"GOOGLE_APPLICATION_CREDENTIALS": false, "VAULT_TOKEN": false, "KUBECONFIG": false,
	} {
		if got := keepAgentEnv(name); got != keep {
			t.Errorf("keepAgentEnv(%s) = %v, want %v", name, got, keep)
		}
	}
}

func TestTaskPayloadRefusesRequirementsItCannotHonour(t *testing.T) {
	for name, payload := range map[string]gitagent.TaskPayload{
		"an unknown requirement":           {Requires: []string{"teleport"}},
		"tools without declaring the need": {Tools: &gitagent.TaskTools{Name: "captain", Path: "/mcp"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := payload.CheckRequirements(); err == nil {
				t.Fatal("CheckRequirements accepted a payload this runner cannot honour")
			}
		})
	}
	if err := (gitagent.TaskPayload{Tools: &gitagent.TaskTools{Name: "captain", Path: "/mcp"}, Requires: []string{gitagent.RequiresTools}}).CheckRequirements(); err != nil {
		t.Fatalf("a payload requiring tools: %v", err)
	}
}
