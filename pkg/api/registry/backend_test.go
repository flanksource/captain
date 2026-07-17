package registry

import (
	"reflect"
	"testing"
)

func TestInferBackend(t *testing.T) {
	cases := map[string]Backend{
		"claude-sonnet-4-6": BackendAnthropic,
		"claude-agent-opus": BackendClaudeAgent,
		"claude-code-x":     BackendClaudeCLI,
		"opus-4-8":          BackendAnthropic,
		"sonnet":            BackendAnthropic,
		"gpt-5.5":           BackendOpenAI,
		"o3":                BackendOpenAI,
		"gemini-2.5-pro":    BackendGemini,
		"gemini-cli-pro":    BackendGeminiCLI,
		"codex-gpt-5-codex": BackendCodexCLI,
		"deepseek-chat":     BackendDeepSeek,
	}
	for model, want := range cases {
		got, err := InferBackend(model)
		if err != nil || got != want {
			t.Errorf("InferBackend(%q) = %q, %v; want %q", model, got, err, want)
		}
	}
	if _, err := InferBackend("totally-unknown"); err == nil {
		t.Error("InferBackend(unknown) should fail loud")
	}
	// grok is no longer claimed by any provider (the codex CLI's grok mode was
	// removed). Pin the removal: a silent return of grok claiming would otherwise
	// resurrect a backend captain no longer routes.
	if _, err := InferBackend("grok-2"); err == nil {
		t.Error("InferBackend(grok-2) should fail loud: grok mode was removed")
	}
}

func TestEffortValidateIncludesCodexTiers(t *testing.T) {
	for _, e := range []Effort{EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra} {
		if err := e.Validate(); err != nil {
			t.Errorf("Effort(%q).Validate() = %v, want nil", e, err)
		}
	}
	if err := Effort("extreme").Validate(); err == nil {
		t.Error("Effort(extreme).Validate() should fail")
	}
	if want := []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra}; !reflect.DeepEqual(AllEfforts(), want) {
		t.Errorf("AllEfforts() = %v, want %v", AllEfforts(), want)
	}
}

func TestBackendKindAndAuth(t *testing.T) {
	if BackendAnthropic.Kind() != "api" || BackendClaudeAgent.Kind() != "cli" {
		t.Errorf("Kind() classification wrong: api=%q cli=%q", BackendAnthropic.Kind(), BackendClaudeAgent.Kind())
	}
	if got := AuthEnvVars(BackendGemini); !reflect.DeepEqual(got, []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}) {
		t.Errorf("AuthEnvVars(gemini) = %v", got)
	}
	if !BackendOpenAI.Valid() || Backend("nope").Valid() {
		t.Error("Backend.Valid() wrong")
	}
}

func TestBackendProviderAndAgents(t *testing.T) {
	tests := map[Backend]Backend{
		BackendAnthropic:   AnthropicProvider,
		BackendClaudeCLI:   AnthropicProvider,
		BackendClaudeAgent: AnthropicProvider,
		BackendClaudeCmux:  AnthropicProvider,
		BackendOpenAI:      OpenAIProvider,
		BackendCodexCLI:    OpenAIProvider,
		BackendCodexAgent:  OpenAIProvider,
		BackendCodexCmux:   OpenAIProvider,
		BackendGemini:      GeminiProvider,
		BackendGeminiCLI:   GeminiProvider,
		BackendDeepSeek:    DeepSeekProvider,
	}
	for backend, want := range tests {
		if got := backend.Provider(); got != want {
			t.Errorf("%s.Provider() = %q, want %q", backend, got, want)
		}
	}
	// Provider.Backends replaces AgentsForProvider. The two lists it unifies
	// disagreed on order (AgentsForProvider put the CLI before the agent, the
	// wildcard fan-out the reverse); the user-visible wildcard order wins.
	if got := OpenAI.Backends(); !reflect.DeepEqual(got, []Backend{
		BackendOpenAI, BackendCodexAgent, BackendCodexCLI, BackendCodexCmux,
	}) {
		t.Fatalf("OpenAI.Backends() = %v", got)
	}
}
