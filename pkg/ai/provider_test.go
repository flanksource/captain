package ai

import (
	"strings"
	"testing"
)

// Every runtime the registry enumerates is a real provider×mode cell, and the
// list users are shown in error hints reaches all of them. It groups by family
// ("anthropic: api, agent, …") because a runtime has no single-token spelling —
// that spelling was the composite id this vocabulary replaced.
func TestRuntimeListReachesEveryRuntime(t *testing.T) {
	runtimes := AllRuntimes()
	if len(runtimes) == 0 {
		t.Fatal("AllRuntimes() is empty")
	}
	list := RuntimeList()
	for _, r := range runtimes {
		if !r.Valid() {
			t.Errorf("AllRuntimes() yielded %v, which is not a served cell", r)
		}
		if !strings.Contains(list, r.Provider) || !strings.Contains(list, string(r.Mode)) {
			t.Errorf("RuntimeList() = %q does not reach %v", list, r)
		}
	}
}

// A mode authenticates with its provider's credential: the CLI and agent modes
// share the API key of the family that owns them, and the cmux modes are
// deliberately keyless because they ride the local CLI's own login.
func TestGetAPIKeyFromEnvPrefersFirstSet(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "from-google")
	if got := GetAPIKeyFromEnv(Google, ModeAPI); got != "from-google" {
		t.Errorf("GetAPIKeyFromEnv(google, api) = %q, want fallback to GOOGLE_API_KEY", got)
	}

	t.Setenv("ANTHROPIC_API_KEY", "ant-123")
	if got := GetAPIKeyFromEnv(Anthropic, ModeCLI); got != "ant-123" {
		t.Errorf("GetAPIKeyFromEnv(anthropic, cli) = %q, want the family's ANTHROPIC_API_KEY", got)
	}

	t.Setenv("OPENAI_API_KEY", "openai-123")
	if got := GetAPIKeyFromEnv(Anthropic, ModeCmux); got != "" {
		t.Errorf("GetAPIKeyFromEnv(anthropic, cmux) = %q, want keyless", got)
	}
	if got := GetAPIKeyFromEnv(OpenAI, ModeCmux); got != "" {
		t.Errorf("GetAPIKeyFromEnv(openai, cmux) = %q, want keyless", got)
	}
}

// A model name names a family. It used to name a runtime too, and the mode it
// smuggled overrode the one the caller chose.
func TestProviderForClaimsFamilyOnly(t *testing.T) {
	cases := map[string]*ModelProvider{
		"claude-sonnet-4":   Anthropic,
		"claude-agent-opus": Anthropic,
		"gemini-2.0-flash":  Google,
		"gemini-cli-pro":    Google,
		"gpt-4o":            OpenAI,
		"codex-mini":        OpenAI,
		"deepseek-reasoner": DeepSeek,
	}
	for model, want := range cases {
		got, err := ProviderFor(model)
		if err != nil {
			t.Errorf("ProviderFor(%q) error: %v", model, err)
			continue
		}
		if got != want {
			t.Errorf("ProviderFor(%q) = %s, want %s", model, got.Name, want.Name)
		}
	}
	if _, err := ProviderFor("totally-unknown-model"); err == nil {
		t.Fatal("ProviderFor(unknown) = nil error, want failure")
	}
}
