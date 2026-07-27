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

func TestBackendKind(t *testing.T) {
	api := map[Backend]bool{BackendAnthropic: true, BackendGemini: true, BackendOpenAI: true, BackendDeepSeek: true}
	for _, b := range AllBackends() {
		want := "cli"
		if api[b] {
			want = "api"
		}
		if got := b.Kind(); got != want {
			t.Errorf("Backend(%q).Kind() = %q, want %q", b, got, want)
		}
	}
}

// TestAuthEnvVars pins each backend to the env vars it authenticates with.
// Cmux backends are intentionally keyless: they use the local CLI login.
func TestAuthEnvVars(t *testing.T) {
	cases := map[Backend][]string{
		BackendAnthropic:   {"ANTHROPIC_API_KEY"},
		BackendClaudeCLI:   {"ANTHROPIC_API_KEY"},
		BackendClaudeAgent: {"ANTHROPIC_API_KEY"},
		BackendClaudeCmux:  nil,
		BackendOpenAI:      {"OPENAI_API_KEY"},
		BackendCodexCLI:    {"OPENAI_API_KEY"},
		BackendCodexAgent:  {"OPENAI_API_KEY"},
		BackendCodexCmux:   nil,
		BackendDeepSeek:    {"DEEPSEEK_API_KEY"},
		BackendGemini:      {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		BackendGeminiCLI:   {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	}
	for _, b := range AllBackends() {
		want, ok := cases[b]
		if !ok {
			t.Errorf("backend %q has no AuthEnvVars expectation in test", b)
			continue
		}
		got := AuthEnvVars(b)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("AuthEnvVars(%q) = %v, want %v", b, got, want)
		}
	}
}

// TestGetAPIKeyFromEnvPrefersFirstSet verifies the priority order and that a CLI
// backend resolves the same key as its parent API backend.
func TestGetAPIKeyFromEnvPrefersFirstSet(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "from-google")
	if got := GetAPIKeyFromEnv(BackendGemini); got != "from-google" {
		t.Errorf("GetAPIKeyFromEnv(gemini) = %q, want fallback to GOOGLE_API_KEY", got)
	}

	t.Setenv("ANTHROPIC_API_KEY", "ant-123")
	if got := GetAPIKeyFromEnv(BackendClaudeCLI); got != "ant-123" {
		t.Errorf("GetAPIKeyFromEnv(claude-cli) = %q, want claude-cli to share ANTHROPIC_API_KEY", got)
	}

	t.Setenv("OPENAI_API_KEY", "openai-123")
	if got := GetAPIKeyFromEnv(BackendClaudeCmux); got != "" {
		t.Errorf("GetAPIKeyFromEnv(claude-cmux) = %q, want keyless cmux", got)
	}
	if got := GetAPIKeyFromEnv(BackendCodexCmux); got != "" {
		t.Errorf("GetAPIKeyFromEnv(codex-cmux) = %q, want keyless cmux", got)
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
		"deepseek-reasoner":   BackendDeepSeek,
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
