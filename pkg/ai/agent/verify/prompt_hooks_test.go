package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// judgeStubProvider records the request it judged; no live model call.
type judgeStubProvider struct{ requests []ai.Request }

func (p *judgeStubProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.requests = append(p.requests, req)
	return &ai.Response{Text: `{"ok":false,"reason":"stub","feedback":"try again"}`}, nil
}
func (p *judgeStubProvider) GetModel() string        { return "stub" }
func (p *judgeStubProvider) GetBackend() api.Backend { return api.BackendAnthropic }

func writeJudgePrompt(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "review-diff.prompt")
	body := "{{role \"user\"}}\nJudge the work in {{cwd}}."
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPromptHooksForWorkflow(t *testing.T) {
	provider := &judgeStubProvider{}

	t.Run("nothing declared yields no hooks", func(t *testing.T) {
		if hooks, err := PromptHooksForWorkflow(nil, provider); err != nil || hooks != nil {
			t.Fatalf("hooks = %v, err = %v", hooks, err)
		}
		if hooks, err := PromptHooksForWorkflow(&api.Workflow{Verify: &api.Verify{}}, provider); err != nil || hooks != nil {
			t.Fatalf("hooks = %v, err = %v", hooks, err)
		}
	})

	t.Run("builds a named LLM judge per prompt", func(t *testing.T) {
		path := writeJudgePrompt(t)
		wf := &api.Workflow{Verify: &api.Verify{Prompts: []string{path, "  "}}}

		hooks, err := PromptHooksForWorkflow(wf, provider)
		if err != nil {
			t.Fatal(err)
		}
		if len(hooks) != 1 {
			t.Fatalf("want 1 hook (blank skipped), got %d", len(hooks))
		}
		plugin, ok := hooks[0].(*Plugin)
		if !ok || plugin.Name() != "judge:"+path {
			t.Fatalf("unexpected hook %#v", hooks[0])
		}
		if _, ok := plugin.v.(*LLMJudgeVerifier); !ok {
			t.Fatalf("verifier = %T, want *LLMJudgeVerifier", plugin.v)
		}
	})

	t.Run("the judge consults the provider, not a live model", func(t *testing.T) {
		path := writeJudgePrompt(t)
		hooks, err := PromptHooksForWorkflow(&api.Workflow{Verify: &api.Verify{Prompts: []string{path}}}, provider)
		if err != nil {
			t.Fatal(err)
		}
		judge := hooks[0].(*Plugin).v

		before := len(provider.requests)
		if _, err := judge.Verify(context.Background(), "/work", nil); err != nil {
			t.Fatal(err)
		}
		if len(provider.requests) != before+1 {
			t.Fatalf("provider judged %d times, want 1", len(provider.requests)-before)
		}
		if user := provider.requests[len(provider.requests)-1].Prompt.User; !strings.Contains(user, "/work") {
			t.Fatalf("judge prompt %q must render the run's cwd", user)
		}
	})

	t.Run("a missing prompt file is an error, not a skipped check", func(t *testing.T) {
		_, err := PromptHooksForWorkflow(&api.Workflow{Verify: &api.Verify{Prompts: []string{"/does/not/exist.prompt"}}}, provider)
		if err == nil || !strings.Contains(err.Error(), "/does/not/exist.prompt") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a judge declaring a sandbox is rejected, not silently ignored", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "judge.prompt")
		body := "---\nsandbox: git-agent\n---\n{{role \"user\"}}\nJudge {{cwd}}."
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := PromptHooksForWorkflow(&api.Workflow{Verify: &api.Verify{Prompts: []string{path}}}, provider)
		if err == nil || !strings.Contains(err.Error(), "declares a sandbox") {
			t.Fatalf("err = %v, want sandbox declaration rejected (R5.4)", err)
		}
	})

	t.Run("a judge declaring a different model is rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "judge.prompt")
		body := "---\nmodel: gpt-5.5\n---\n{{role \"user\"}}\nJudge {{cwd}}."
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := PromptHooksForWorkflow(&api.Workflow{Verify: &api.Verify{Prompts: []string{path}}}, provider)
		if err == nil || !strings.Contains(err.Error(), `declares model "gpt-5.5"`) {
			t.Fatalf("err = %v, want model mismatch rejected", err)
		}
	})

	t.Run("a judge matching the provider's model is accepted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "judge.prompt")
		body := "---\nmodel: stub\n---\n{{role \"user\"}}\nJudge {{cwd}}."
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		hooks, err := PromptHooksForWorkflow(&api.Workflow{Verify: &api.Verify{Prompts: []string{path}}}, provider)
		if err != nil || len(hooks) != 1 {
			t.Fatalf("hooks = %v, err = %v", hooks, err)
		}
	})

	t.Run("declared prompts with no provider fail loud", func(t *testing.T) {
		path := writeJudgePrompt(t)
		_, err := PromptHooksForWorkflow(&api.Workflow{Verify: &api.Verify{Prompts: []string{path}}}, nil)
		if err == nil || !strings.Contains(err.Error(), "no provider") {
			t.Fatalf("err = %v", err)
		}
	})
}
