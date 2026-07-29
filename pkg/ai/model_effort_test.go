package ai

import (
	"testing"
)

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
