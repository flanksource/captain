package cmux

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

// New receives a model that has already been resolved, so it derives the agent
// from the provider and passes the id through untouched. It used to re-normalize
// the id itself, which is how the id captain recorded and the id the CLI received
// could differ.
func TestNewDerivesAgentFromTheResolvedProvider(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		wantModel string
		wantAgent string
		want      api.Runtime
	}{
		{"claude cmux", "cmux:opus", "claude-opus-5", "claude", api.RuntimeOf(api.Anthropic, api.ModeCmux)},
		{"codex cmux", "cmux:gpt-5", "gpt-5", "codex", api.RuntimeOf(api.OpenAI, api.ModeCmux)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, err := ai.Resolve(api.Model{Name: tc.model})
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.model, err)
			}
			p, err := New(ai.Config{Model: model})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if p.agent != tc.wantAgent {
				t.Fatalf("agent = %q, want %q", p.agent, tc.wantAgent)
			}
			if p.GetModel() != tc.wantModel {
				t.Fatalf("GetModel() = %q, want %q", p.GetModel(), tc.wantModel)
			}
			if p.GetRuntime() != tc.want {
				t.Fatalf("GetRuntime() = %q, want %q", p.GetRuntime(), tc.want)
			}
		})
	}
}

func TestNewRejectsAProviderWithNoCmuxAgent(t *testing.T) {
	model, err := ai.Resolve(api.Model{Name: "api:gemini-2.5-pro"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := New(ai.Config{Model: model}); err == nil {
		t.Fatal("New() error = nil, want an error for a family with no cmux agent")
	}
}

func TestExecuteStreamRequiresPrompt(t *testing.T) {
	model, err := ai.Resolve(api.Model{Name: "cmux:claude"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	p, err := New(ai.Config{Model: model})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := p.ExecuteStream(context.Background(), ai.Request{Prompt: api.Prompt{User: ""}}); err == nil {
		t.Fatal("ExecuteStream() error = nil, want a required-prompt error")
	}
}

func TestUsageFromStats(t *testing.T) {
	stats := SessionStats{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, CacheCreationTokens: 7}
	got := usageFromStats(stats)
	want := ai.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, CacheWriteTokens: 7}
	if got != want {
		t.Fatalf("usageFromStats() = %+v, want %+v", got, want)
	}
}

func TestExecuteFailsLoudWhenCmuxUnavailable(t *testing.T) {
	// A runner that fails `ping` makes the whole run fail before any cmux surface is
	// created; Execute must surface that as a CLI execution failure, never success.
	model, err := ai.Resolve(api.Model{Name: "cmux:claude"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	p, err := New(ai.Config{Model: model})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	r := newTestRun(runConfig{}, func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
		return "", errors.New("cmux not running")
	})
	if _, _, err := p.execute(context.Background(), ai.Request{Prompt: api.Prompt{User: "hi"}, Setup: &shell.Setup{Cwd: t.TempDir()}}, r); err == nil {
		t.Fatal("execute() error = nil, want a failure when cmux ping fails")
	}
}
