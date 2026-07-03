package api

import (
	"reflect"
	"testing"
)

func TestInferBackend(t *testing.T) {
	cases := map[string]Backend{
		"claude-sonnet-4-6": BackendAnthropic,
		"claude-agent-opus": BackendClaudeAgent,
		"claude-code-x":     BackendClaudeCLI,
		"gpt-5.5":           BackendOpenAI,
		"o3":                BackendOpenAI,
		"gemini-2.5-pro":    BackendGemini,
		"gemini-cli-pro":    BackendGeminiCLI,
		"codex-gpt-5-codex": BackendCodexCLI,
		"grok-2":            BackendCodexCLI,
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
}

func TestEffortValidateIncludesXHigh(t *testing.T) {
	for _, e := range []Effort{EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh} {
		if err := e.Validate(); err != nil {
			t.Errorf("Effort(%q).Validate() = %v, want nil", e, err)
		}
	}
	if err := Effort("max").Validate(); err == nil {
		t.Error("Effort(max).Validate() should fail (only low/medium/high/xhigh)")
	}
	if want := []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh}; !reflect.DeepEqual(AllEfforts(), want) {
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

func TestPermissionModeValid(t *testing.T) {
	if !PermissionMode("").Valid() || !PermissionAcceptEdits.Valid() || PermissionMode("yolo").Valid() {
		t.Error("PermissionMode.Valid() wrong")
	}
}
