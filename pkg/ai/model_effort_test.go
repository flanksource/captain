package ai

import (
	"testing"
)

func TestRegistryGPT56Availability(t *testing.T) {
	if _, ok := RegistryModelDef(OpenAI, ModeAPI, "gpt-5.6"); !ok {
		t.Fatal("gpt-5.6 should be available to OpenAI")
	}
	if _, ok := RegistryModelDef(OpenAI, ModeAgent, "gpt-5.6"); ok {
		t.Fatal("gpt-5.6 API base should not be available to Codex")
	}
	if _, ok := RegistryModelDef(OpenAI, ModeAgent, "sol"); !ok {
		t.Fatal("Sol should be available to Codex")
	}
	if _, ok := RegistryModelDef(OpenAI, ModeAPI, "sol"); !ok {
		t.Fatal("Sol should be available to OpenAI")
	}
}
