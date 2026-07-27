package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

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
