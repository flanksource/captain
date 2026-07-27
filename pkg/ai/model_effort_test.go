package ai

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestValidateModelEffort(t *testing.T) {
	tests := []struct {
		name    string
		backend Backend
		model   string
		effort  api.Effort
		wantErr string
	}{
		{"api max", BackendOpenAI, "gpt-5.6", api.EffortMax, ""},
		{"api rejects ultra", BackendOpenAI, "gpt-5.6", api.EffortUltra, "does not support"},
		{"sol max", BackendCodexAgent, "sol", api.EffortMax, ""},
		{"sol ultra", BackendCodexAgent, "sol", api.EffortUltra, ""},
		{"terra ultra", BackendCodexCmux, "gpt-5.6-terra", api.EffortUltra, ""},
		{"luna supports max", BackendCodexCLI, "luna", api.EffortMax, ""},
		{"gpt55 rejects max", BackendCodexAgent, "gpt-5.5", api.EffortMax, "does not support"},
		{"unknown codex remains open", BackendCodexAgent, "gpt-future", api.EffortUltra, ""},
		{"adaptive Anthropic supports max", BackendAnthropic, "claude-sonnet-5", api.EffortMax, ""},
		{"DeepSeek rejects configured effort", BackendDeepSeek, "deepseek-v4-pro", api.EffortHigh, "does not support a reasoning effort"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateModelEffort(tt.backend, tt.model, tt.effort)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateModelEffort: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRegistryGPT56Availability(t *testing.T) {
	if _, ok := RegistryModelDef(BackendOpenAI, "gpt-5.6"); !ok {
		t.Fatal("gpt-5.6 should be available to OpenAI")
	}
	if _, ok := RegistryModelDef(BackendCodexAgent, "gpt-5.6"); ok {
		t.Fatal("gpt-5.6 API base should not be available to Codex")
	}
	if _, ok := RegistryModelDef(BackendCodexAgent, "sol"); !ok {
		t.Fatal("Sol should be available to Codex")
	}
	if _, ok := RegistryModelDef(BackendOpenAI, "sol"); !ok {
		t.Fatal("Sol should be available to OpenAI")
	}
}
