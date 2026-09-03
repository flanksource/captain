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
func (p *judgeStubProvider) GetModel() string { return "stub" }
func (p *judgeStubProvider) GetRuntime() api.Runtime {
	return api.RuntimeOf(api.Anthropic, api.ModeAPI)
}

func writeJudgePrompt(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "review-diff.prompt")
	body := "{{role \"user\"}}\nJudge the work in {{cwd}}."
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// judgeHooks builds the hooks for a workflow that declares only prompts.
func judgeHooks(t *testing.T, provider ai.Provider, prompts ...string) ([]any, error) {
	t.Helper()
	return HooksFor(context.Background(), &api.Workflow{Verify: &api.Verify{Prompts: prompts}}, Options{Provider: provider})
}

func TestPromptHooks(t *testing.T) {
	provider := &judgeStubProvider{}

	t.Run("nothing declared yields no hooks", func(t *testing.T) {
		if hooks, err := HooksFor(context.Background(), nil, Options{Provider: provider}); err != nil || hooks != nil {
			t.Fatalf("hooks = %v, err = %v", hooks, err)
		}
		if hooks, err := judgeHooks(t, provider); err != nil || hooks != nil {
			t.Fatalf("hooks = %v, err = %v", hooks, err)
		}
	})

	t.Run("a blank prompt entry fails instead of dropping the check", func(t *testing.T) {
		_, err := judgeHooks(t, provider, writeJudgePrompt(t), "  ")
		if err == nil || !strings.Contains(err.Error(), "prompts[1] is empty") {
			t.Fatalf("err = %v, want blank entry rejected", err)
		}
	})

	t.Run("builds a named LLM judge per prompt", func(t *testing.T) {
		path := writeJudgePrompt(t)
		hooks, err := judgeHooks(t, provider, path)
		if err != nil {
			t.Fatal(err)
		}
		if len(hooks) != 1 {
			t.Fatalf("want 1 hook, got %d", len(hooks))
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
		hooks, err := judgeHooks(t, provider, writeJudgePrompt(t))
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

	// The node's framework names the verifier family that produced it, using the
	// same kind string the report carries — a renderer grouping a mixed tree by
	// framework must not see "judge" and "prompt" as two families.
	t.Run("the judgement is one node in the prompt framework", func(t *testing.T) {
		hooks, err := judgeHooks(t, provider, writeJudgePrompt(t))
		if err != nil {
			t.Fatal(err)
		}
		vd, err := hooks[0].(*Plugin).v.Verify(context.Background(), "/work", nil)
		if err != nil {
			t.Fatal(err)
		}
		if vd.Report.Kind != api.VerifyKindPrompt {
			t.Fatalf("report kind = %q, want %q", vd.Report.Kind, api.VerifyKindPrompt)
		}
		if got := vd.Report.Tests[0].Framework; got != api.VerifyKindPrompt {
			t.Fatalf("node framework = %q, want %q — the kind string, not a second name for it", got, api.VerifyKindPrompt)
		}
	})

	t.Run("a missing prompt file is an error, not a skipped check", func(t *testing.T) {
		_, err := judgeHooks(t, provider, "/does/not/exist.prompt")
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
		_, err := judgeHooks(t, provider, path)
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
		_, err := judgeHooks(t, provider, path)
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
		hooks, err := judgeHooks(t, provider, path)
		if err != nil || len(hooks) != 1 {
			t.Fatalf("hooks = %v, err = %v", hooks, err)
		}
	})

	t.Run("declared prompts with no provider fail loud", func(t *testing.T) {
		_, err := judgeHooks(t, nil, writeJudgePrompt(t))
		if err == nil || !strings.Contains(err.Error(), "no provider") {
			t.Fatalf("err = %v", err)
		}
	})
}
