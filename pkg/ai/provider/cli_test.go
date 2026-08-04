package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/sandbox/adapter"
	"github.com/flanksource/commons-db/shell"
	sandboxruntime "github.com/flanksource/sandbox-runtime/sandbox"
)

type fakeCommandSandbox struct {
	commandErr error
	executable string
	closed     bool
}

func (f *fakeCommandSandbox) Command(ctx context.Context, command string, args ...string) (*exec.Cmd, error) {
	if f.commandErr != nil {
		return nil, f.commandErr
	}
	if f.executable != "" {
		command = f.executable
	}
	return exec.CommandContext(ctx, command, args...), nil
}

func (f *fakeCommandSandbox) Close(context.Context) error {
	f.closed = true
	return nil
}

func TestParseStderr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"error line", "something\nerror: bad thing\nmore", "error: bad thing"},
		{"no errors", "line1\nline2", "line1; line2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStderr(tt.input)
			if got != tt.want {
				t.Errorf("ParseStderr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGeminiCLIUsesContextDir(t *testing.T) {
	original := adapter.NewSRTRuntime
	fake := &fakeCommandSandbox{}
	adapter.NewSRTRuntime = func(context.Context, sandboxruntime.Config) (adapter.Runtime, error) {
		return fake, nil
	}
	t.Cleanup(func() { adapter.NewSRTRuntime = original })

	cwd := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	binDir := t.TempDir()
	gemini := filepath.Join(binDir, "gemini")
	// A fake gemini speaking --output-format stream-json: it reports its own cwd
	// as the assistant reply, then closes the run with a terminal result.
	script := "#!/bin/sh\ncat >/dev/null\n" +
		"printf '{\"type\":\"init\",\"session_id\":\"sess-1\",\"model\":\"gemini-3.5-flash\"}\\n'\n" +
		"printf '{\"type\":\"message\",\"role\":\"assistant\",\"content\":\"%s\",\"delta\":true}\\n' \"$(pwd)\"\n" +
		"printf '{\"type\":\"result\",\"status\":\"success\",\"stats\":{\"input_tokens\":3,\"output_tokens\":2,\"cached\":2}}\\n'\n"
	if err := os.WriteFile(gemini, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gemini: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	provider := NewGeminiCLI("gemini-cli-pro")
	provider.sandbox = &api.SandboxConfig{Kind: api.SandboxSRT}
	resp, err := provider.Execute(context.Background(), ai.Request{
		Prompt: api.Prompt{User: "hello"},
		Setup:  &shell.Setup{Cwd: cwd},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, err := filepath.EvalSymlinks(filepath.Clean(strings.TrimSpace(resp.Text)))
	if err != nil {
		t.Fatalf("eval response text: %v", err)
	}
	want, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("eval cwd: %v", err)
	}
	if got != want {
		t.Errorf("response text = %q, want %q", got, want)
	}
	if resp.Usage.InputTokens != 1 || resp.Usage.CacheReadTokens != 2 || resp.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v, want input=1 cacheRead=2 output=2", resp.Usage)
	}
	if !fake.closed {
		t.Fatal("sandbox was not closed after the CLI stream completed")
	}
}

func TestCLIProviderFactoriesCarrySandbox(t *testing.T) {
	srtSelected := func(sandbox *api.SandboxConfig) bool {
		return sandbox != nil && sandbox.Kind == api.SandboxSRT
	}
	tests := []struct {
		backend api.Backend
		model   string
		enabled func(api.Provider) bool
	}{
		{api.BackendClaudeCLI, "claude-sonnet-5", func(p api.Provider) bool { return srtSelected(p.(*ClaudeCLI).sandbox) }},
		{api.BackendCodexCLI, "gpt-5.5", func(p api.Provider) bool { return srtSelected(p.(*CodexCLI).sandbox) }},
		{api.BackendGeminiCLI, "gemini-3.5-flash", func(p api.Provider) bool { return srtSelected(p.(*GeminiCLI).sandbox) }},
	}
	for _, tt := range tests {
		t.Run(string(tt.backend), func(t *testing.T) {
			provider, err := api.NewProvider(api.Config{Model: api.Model{Name: tt.model, Backend: tt.backend}, Sandbox: true})
			if err != nil {
				t.Fatal(err)
			}
			if !tt.enabled(provider) {
				t.Fatal("provider did not retain the sandbox setting")
			}
		})
	}
}

func TestSandboxCommandFailuresClose(t *testing.T) {
	original := adapter.NewSRTRuntime
	t.Cleanup(func() { adapter.NewSRTRuntime = original })

	srt := &api.SandboxConfig{Kind: api.SandboxSRT}
	fake := &fakeCommandSandbox{commandErr: errors.New("wrap failed")}
	adapter.NewSRTRuntime = func(context.Context, sandboxruntime.Config) (adapter.Runtime, error) { return fake, nil }
	if _, _, err := newCLICommand(context.Background(), "codex", nil, t.TempDir(), srt); err == nil || !strings.Contains(err.Error(), "wrap codex") {
		t.Fatalf("err = %v, want sandbox wrapping failure", err)
	}
	if !fake.closed {
		t.Fatal("sandbox was not closed after wrapping failed")
	}

	fake = &fakeCommandSandbox{executable: "captain-command-that-does-not-exist"}
	adapter.NewSRTRuntime = func(context.Context, sandboxruntime.Config) (adapter.Runtime, error) { return fake, nil }
	if _, _, _, _, err := startCLIStream(context.Background(), "codex", nil, nil, t.TempDir(), nil, srt); err == nil {
		t.Fatal("expected process start failure")
	}
	if !fake.closed {
		t.Fatal("sandbox was not closed after process start failed")
	}
}
