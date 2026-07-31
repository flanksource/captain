package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	sandboxruntime "github.com/flanksource/sandbox-runtime/sandbox"
)

type fakeCommandSandbox struct {
	command string
	args    []string
	closed  bool
}

func (f *fakeCommandSandbox) Command(ctx context.Context, command string, args ...string) (*exec.Cmd, error) {
	f.command = command
	f.args = append([]string{}, args...)
	return exec.CommandContext(ctx, "true"), nil
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

	resp, err := NewGeminiCLI("gemini-cli-pro").Execute(context.Background(), ai.Request{
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
}

func TestNewCLICommandUsesSandboxRuntime(t *testing.T) {
	original := newCommandSandbox
	t.Cleanup(func() { newCommandSandbox = original })

	cwd := t.TempDir()
	fake := &fakeCommandSandbox{}
	var cfg sandboxruntime.Config
	newCommandSandbox = func(_ context.Context, got sandboxruntime.Config) (commandSandbox, error) {
		cfg = got
		return fake, nil
	}

	_, closeSandbox, err := newCLICommand(context.Background(), "codex", []string{"exec", "--json"}, cwd, true)
	if err != nil {
		t.Fatalf("newCLICommand: %v", err)
	}
	if fake.command != "codex" || strings.Join(fake.args, " ") != "exec --json" {
		t.Fatalf("sandbox command = %q %v", fake.command, fake.args)
	}
	if cfg.Filesystem.AllowWrite[0] != cwd {
		t.Fatalf("first writable path = %q, want workspace %q", cfg.Filesystem.AllowWrite[0], cwd)
	}
	if !containsString(cfg.Network.AllowedDomains, "*.openai.com") {
		t.Fatalf("allowed domains = %v, want OpenAI endpoints", cfg.Network.AllowedDomains)
	}
	if !containsString(cfg.Filesystem.DenyRead, filepath.Join(os.Getenv("HOME"), ".ssh")) {
		t.Fatalf("deny-read policy = %v, want .ssh protected", cfg.Filesystem.DenyRead)
	}
	if !containsString(cfg.Filesystem.DenyRead, "/var/run/docker.sock") {
		t.Fatalf("deny-read policy = %v, want Docker socket protected", cfg.Filesystem.DenyRead)
	}

	closeSandbox()
	if !fake.closed {
		t.Fatal("sandbox was not closed")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
