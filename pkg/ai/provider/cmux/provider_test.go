package cmux

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons/logger"
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

// TestExecuteLaunchesClaudeWithTranslatedToolLists drives execute up to the
// agent launch and pins that the command typed into the surface carries
// claude's own tool names rather than the portable keys the spec authored.
func TestExecuteLaunchesClaudeWithTranslatedToolLists(t *testing.T) {
	model, err := ai.Resolve(api.Model{Name: "cmux:claude"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	p, err := New(ai.Config{Model: model})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var launched string
	stop := errors.New("stop after the agent command")
	r := newTestRun(runConfig{
		Timeout: time.Second, SendAttempts: 1,
		ScreenPollInterval: time.Millisecond, ScreenMaxPollInterval: time.Millisecond, ScreenStableDuration: time.Millisecond,
	}, func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
		switch args[0] {
		case "list-workspaces":
			return `{"workspaces":[]}`, nil
		case "new-workspace":
			return "workspace:1", nil
		case "new-surface":
			return "surface:1", nil
		case "read-screen":
			return "$ ", nil
		case "send":
			launched = args[len(args)-1]
			return "", stop
		}
		return "ok", nil
	})
	req := ai.Request{
		Prompt:      api.Prompt{User: "hi"},
		Setup:       &shell.Setup{Cwd: t.TempDir()},
		Permissions: api.Permissions{Tools: api.Tools{"shell": api.ToolPolicyDeny, "read": api.ToolPolicyAllow, "NotATool": api.ToolPolicyDeny}},
	}
	prev := logger.GetOutput()
	t.Cleanup(func() { logger.SetOutput(prev) })
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	if _, _, err := p.execute(context.Background(), req, r); !errors.Is(err, stop) {
		t.Fatalf("execute() error = %v, want the launch to reach send", err)
	}
	for _, want := range []string{"--disallowedTools Bash,BashOutput,KillShell,Monitor", "--allowedTools Read"} {
		if !strings.Contains(launched, want) {
			t.Errorf("agent command %q does not contain %q", launched, want)
		}
	}
	if want := `permissions.tools "NotATool" deny is ignored: anthropic cmux has no tool it names`; !strings.Contains(logs.String(), want) {
		t.Errorf("log output missing %q\n--- output ---\n%s", want, logs.String())
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
