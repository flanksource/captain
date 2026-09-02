package ai

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

func findModel(t *testing.T, models []Model, id string) Model {
	t.Helper()
	for _, m := range models {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("model %q not in catalog %+v", id, modelIDsFrom(models))
	return Model{}
}

func TestMergeLiveCatalogUpsertsLiveAndPreservesStatic(t *testing.T) {
	static := []Model{
		{ID: "anthropic/claude-sonnet-5", Provider: Anthropic, Mode: ModeAPI, Label: "Claude Sonnet 5", ContextWindow: 1_000_000, ReleaseDate: "2026-06-01"},
		{ID: "claude-opus-4-8", Provider: Anthropic, Mode: ModeAgent, Label: "Claude Agent · Opus 4.8", ContextWindow: 1_000_000},
	}
	adapters := []AdapterStatus{
		{Provider: OpenAI.Name, Mode: string(ModeCLI), Type: "cli", ModelDetails: []ModelDef{
			{ID: "gpt-5.6-sol", Name: "GPT-5.6-Sol", Reasoning: true, ReleaseDate: "2026-07-09", SupportedEfforts: []api.Effort{api.EffortLow, api.EffortMax}},
		}},
		{Provider: Anthropic.Name, Mode: string(ModeAPI), Type: "api", ModelDetails: []ModelDef{
			{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Reasoning: true, ReleaseDate: "2026-06-29"},
		}},
		// Gemini has no agent mode, so its CLI models keep their own runtime row.
		{Provider: Google.Name, Mode: string(ModeCLI), Type: "cli", ModelDetails: []ModelDef{
			{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash"},
		}},
	}

	merged := mergeLiveCatalog(static, adapters, liveCatalogOptions{})

	// A live codex model becomes one codex-agent entry keyed by its exact id.
	sol := findModel(t, merged, "gpt-5.6-sol")
	if sol.Provider != OpenAI || sol.Mode != ModeAgent {
		t.Errorf("sol runtime = %v %s, want openai agent", sol.Provider, sol.Mode)
	}
	if !sol.Reasoning || sol.ReleaseDate != "2026-07-09" || len(sol.SupportedEfforts) != 2 {
		t.Errorf("sol not projected from live probe: %+v", sol)
	}

	// The API entry is upserted: static context window preserved, live release
	// date wins.
	sonnet := findModel(t, merged, "anthropic/claude-sonnet-5")
	if sonnet.ContextWindow != 1_000_000 {
		t.Errorf("sonnet context window = %d, want static 1000000 preserved", sonnet.ContextWindow)
	}
	if sonnet.ReleaseDate != "2026-06-29" {
		t.Errorf("sonnet release date = %q, want live 2026-06-29", sonnet.ReleaseDate)
	}

	// A static entry the probe did not rediscover is retained (shown disabled by
	// LiveCatalogInfo when its provider is unconfigured).
	findModel(t, merged, "claude-opus-4-8")

	gemini := findModel(t, merged, "gemini-3.5-flash")
	if gemini.Provider != Google || gemini.Mode != ModeCLI {
		t.Errorf("gemini runtime = %v %s, want google cli", gemini.Provider, gemini.Mode)
	}
}

func TestLiveCatalogInfoAppliesPerProviderConfigured(t *testing.T) {
	prevProbe, prevAuthProbe := adapterProbe, adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe, adapterAuthProbe = prevProbe, prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""
	home := t.TempDir()
	adapterAuthProbe = func() AuthProbe { return fakeProbe(nil, nil, nil, home) }
	adapterProbe = func(AuthProbe) ([]AdapterStatus, error) {
		return []AdapterStatus{
			{Provider: Anthropic.Name, Mode: string(ModeAPI), Type: "api", ModelDetails: []ModelDef{
				{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Reasoning: true},
			}},
			{Provider: OpenAI.Name, Mode: string(ModeAPI), Type: "api", ModelDetails: []ModelDef{
				{ID: "gpt-5.5", Name: "GPT-5.5", Reasoning: true},
			}},
		}, nil
	}

	infos, err := LiveCatalogInfo([]string{"anthropic"})
	if err != nil {
		t.Fatalf("LiveCatalogInfo: %v", err)
	}
	byID := map[string]ModelInfo{}
	for _, info := range infos {
		byID[info.ID] = info
	}

	anthropic, ok := byID["anthropic/claude-sonnet-5"]
	if !ok || !anthropic.Configured {
		t.Errorf("anthropic model should be configured when its provider key is present: %+v", anthropic)
	}
	if anthropic.Provider != "anthropic" {
		t.Errorf("anthropic provider = %q", anthropic.Provider)
	}
	openai, ok := byID["openai/gpt-5.5"]
	if !ok || openai.Configured {
		t.Errorf("openai model should be unconfigured (no key) but still listed: %+v", openai)
	}
	if openai.Availability.State != api.AvailabilityMissingCredential || openai.Availability.Remediation == "" {
		t.Errorf("openai availability = %+v, want missing credentials with remediation", openai.Availability)
	}
	if !anthropic.Availability.IsAvailable() {
		t.Errorf("anthropic availability = %+v, want available", anthropic.Availability)
	}
}
