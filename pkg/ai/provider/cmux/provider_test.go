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

func TestNewDerivesAgentAndBackend(t *testing.T) {
	cases := []struct {
		name        string
		backend     api.Backend
		model       string
		wantModel   string
		wantAgent   string
		wantBackend api.Backend
	}{
		{"claude cmux", api.BackendClaudeCmux, "opus", "claude-opus-5", "claude", api.BackendClaudeCmux},
		{"codex cmux", api.BackendCodexCmux, "gpt-5", "gpt-5", "codex", api.BackendCodexCmux},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(ai.Config{Model: api.Model{Name: tc.model, Backend: tc.backend}})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if p.agent != tc.wantAgent {
				t.Fatalf("agent = %q, want %q", p.agent, tc.wantAgent)
			}
			if p.GetModel() != tc.wantModel {
				t.Fatalf("GetModel() = %q, want %q", p.GetModel(), tc.wantModel)
			}
			if p.GetBackend() != tc.wantBackend {
				t.Fatalf("GetBackend() = %q, want %q", p.GetBackend(), tc.wantBackend)
			}
		})
	}
}

func TestNewRejectsUnsupportedBackend(t *testing.T) {
	if _, err := New(ai.Config{Model: api.Model{Name: "claude", Backend: api.BackendClaudeAgent}}); err == nil {
		t.Fatal("New() error = nil, want an error for a non-cmux backend")
	}
}

func TestExecuteStreamRequiresPrompt(t *testing.T) {
	p, err := New(ai.Config{Model: api.Model{Name: "claude", Backend: api.BackendClaudeCmux}})
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
	p, err := New(ai.Config{Model: api.Model{Name: "claude", Backend: api.BackendClaudeCmux}})
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
