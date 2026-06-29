package ai

import (
	"strings"
	"testing"
)

func TestBackendValid(t *testing.T) {
	for _, b := range AllBackends() {
		if !b.Valid() {
			t.Errorf("AllBackends()[%q].Valid() = false", b)
		}
	}
	for _, junk := range []Backend{"", "nope", "claude", "anthropic-cli"} {
		if junk.Valid() {
			t.Errorf("Backend(%q).Valid() = true, want false", junk)
		}
	}
}

func TestBackendListContainsEveryBackend(t *testing.T) {
	list := BackendList()
	for _, b := range AllBackends() {
		if !strings.Contains(list, string(b)) {
			t.Errorf("BackendList() = %q is missing %q", list, b)
		}
	}
	// All seven backends are enumerated.
	if got := strings.Count(list, ",") + 1; got != len(AllBackends()) {
		t.Errorf("BackendList() lists %d backends, want %d", got, len(AllBackends()))
	}
}

// TestInferBackendErrorListsAllBackends ensures the "pass --backend explicitly"
// hint stays in sync with AllBackends().
func TestInferBackendErrorListsAllBackends(t *testing.T) {
	_, err := InferBackend("totally-unknown-model")
	if err == nil {
		t.Fatal("InferBackend(unknown) = nil error, want failure")
	}
	for _, b := range AllBackends() {
		if !strings.Contains(err.Error(), string(b)) {
			t.Errorf("InferBackend error %q is missing %q", err.Error(), b)
		}
	}
}

func TestInferBackendKnownPrefixes(t *testing.T) {
	cases := map[string]Backend{
		"claude-agent-sonnet": BackendClaudeAgent,
		"claude-code-opus":    BackendClaudeCLI,
		"claude-sonnet-4":     BackendAnthropic,
		"gemini-2.0-flash":    BackendGemini,
		"gemini-cli-pro":      BackendGeminiCLI,
		"gpt-4o":              BackendOpenAI,
		"codex-mini":          BackendCodexCLI,
	}
	for model, want := range cases {
		got, err := InferBackend(model)
		if err != nil {
			t.Errorf("InferBackend(%q) error: %v", model, err)
			continue
		}
		if got != want {
			t.Errorf("InferBackend(%q) = %q, want %q", model, got, want)
		}
	}
}
