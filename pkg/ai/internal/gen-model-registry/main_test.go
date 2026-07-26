package main

import (
	"encoding/json"
	"testing"
)

func TestApplyPatchesPartialOverlay(t *testing.T) {
	out := map[string]generatedModel{
		"claude-sonnet-5": {
			ID: "claude-sonnet-5", Provider: "anthropic", Family: "sonnet",
			Version: "5", Label: "Claude Sonnet 5", ReleaseDate: "2026-06-29",
			Reasoning: true, ContextWindow: 1000000, Preferred: true,
		},
	}
	patches := map[string]json.RawMessage{
		"claude-sonnet-5": json.RawMessage(`{"reasoning": false, "adaptiveThinking": true}`),
	}
	if err := applyPatches(out, patches); err != nil {
		t.Fatalf("applyPatches: %v", err)
	}
	got := out["claude-sonnet-5"]
	if got.Reasoning != false {
		t.Errorf("Reasoning = %v, want false (patched)", got.Reasoning)
	}
	if !got.AdaptiveThinking {
		t.Errorf("AdaptiveThinking = %v, want true (patched)", got.AdaptiveThinking)
	}
	// Unspecified fields keep their fetched values.
	if got.Preferred != true || got.ContextWindow != 1000000 || got.Label != "Claude Sonnet 5" {
		t.Errorf("unpatched fields were altered: %+v", got)
	}
}

func TestApplyPatchesAddsNewEntry(t *testing.T) {
	out := map[string]generatedModel{}
	patches := map[string]json.RawMessage{
		"gpt-5.5": json.RawMessage(`{"provider": "openai", "family": "gpt", "version": "5.5",
			"label": "GPT-5.5", "reasoning": true, "contextWindow": 1050000, "preferred": true}`),
	}
	if err := applyPatches(out, patches); err != nil {
		t.Fatalf("applyPatches: %v", err)
	}
	got, ok := out["gpt-5.5"]
	if !ok {
		t.Fatal("gpt-5.5 was not added")
	}
	if got.ID != "gpt-5.5" {
		t.Errorf("ID = %q, want gpt-5.5 (derived from patch key)", got.ID)
	}
	if got.Provider != "openai" || got.Family != "gpt" || !got.Reasoning {
		t.Errorf("new entry has wrong fields: %+v", got)
	}
}

func TestApplyPatchesNullRemovesEntry(t *testing.T) {
	out := map[string]generatedModel{
		"deepseek-chat":     {ID: "deepseek-chat", Provider: "deepseek", Family: "deepseek"},
		"deepseek-v4-pro":   {ID: "deepseek-v4-pro", Provider: "deepseek", Family: "deepseek"},
		"deepseek-reasoner": {ID: "deepseek-reasoner", Provider: "deepseek", Family: "deepseek"},
	}
	patches := map[string]json.RawMessage{
		"deepseek-chat":     json.RawMessage(`null`),
		"deepseek-reasoner": json.RawMessage(`null`),
	}
	if err := applyPatches(out, patches); err != nil {
		t.Fatalf("applyPatches: %v", err)
	}
	if _, ok := out["deepseek-chat"]; ok {
		t.Error("deepseek-chat should have been removed by null patch")
	}
	if _, ok := out["deepseek-reasoner"]; ok {
		t.Error("deepseek-reasoner should have been removed by null patch")
	}
	if _, ok := out["deepseek-v4-pro"]; !ok {
		t.Error("deepseek-v4-pro should have been retained")
	}
}

func TestApplyPatchesNewEntryMissingFieldsErrors(t *testing.T) {
	out := map[string]generatedModel{}
	patches := map[string]json.RawMessage{
		"mystery-model": json.RawMessage(`{"reasoning": true}`),
	}
	if err := applyPatches(out, patches); err == nil {
		t.Fatal("expected error for new entry missing provider/family/label, got nil")
	}
}

func TestSupportsTextIORejectsNonPromptableModalities(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  []string
		output []string
		want   bool
	}{
		{"text only", []string{"text"}, []string{"text"}, true},
		{"multimodal input", []string{"text", "image", "audio"}, []string{"text"}, true},
		{"unrecorded modalities", nil, nil, true},
		{"audio-only input", []string{"audio"}, []string{"text"}, false},
		{"image-only output", []string{"text"}, []string{"image"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := modelsDevModel{Modalities: modelsDevModalities{Input: tc.input, Output: tc.output}}
			if got := supportsTextIO(model); got != tc.want {
				t.Errorf("supportsTextIO(in=%v out=%v) = %v, want %v", tc.input, tc.output, got, tc.want)
			}
		})
	}
}

func TestDeriveSupportedEffortsDropsNone(t *testing.T) {
	got := deriveSupportedEfforts("openai", modelsDevModel{ReasoningOptions: []modelsDevReasoningOption{{
		Type:   "effort",
		Values: []string{"none", "minimal", "low", "medium", "max", "max"},
	}}})
	want := []string{"low", "medium", "max"}
	if len(got) != len(want) {
		t.Fatalf("efforts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("efforts = %v, want %v", got, want)
		}
	}
}

func TestDeriveSupportedEffortsKeepsOnlyExecutableProviderLevels(t *testing.T) {
	effort := modelsDevReasoningOption{Type: "effort", Values: []string{"minimal", "low", "medium", "high", "xhigh", "max"}}
	adaptiveAnthropic := modelsDevModel{Reasoning: true, ReasoningOptions: []modelsDevReasoningOption{effort}}
	legacyAnthropic := modelsDevModel{Reasoning: true, ReasoningOptions: []modelsDevReasoningOption{{Type: "budget_tokens"}, effort}}

	if got := deriveSupportedEfforts("anthropic", adaptiveAnthropic); len(got) != 5 || got[4] != "max" {
		t.Errorf("adaptive Anthropic efforts = %v, want low through max", got)
	}
	if got := deriveSupportedEfforts("anthropic", legacyAnthropic); got != nil {
		t.Errorf("legacy Anthropic efforts = %v, want nil", got)
	}
	if got := deriveSupportedEfforts("deepseek", modelsDevModel{ReasoningOptions: []modelsDevReasoningOption{effort}}); got != nil {
		t.Errorf("DeepSeek efforts = %v, want nil", got)
	}
}
