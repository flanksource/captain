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
		{ID: "anthropic/claude-sonnet-5", Backend: BackendAnthropic, Label: "Claude Sonnet 5", ContextWindow: 1_000_000, ReleaseDate: "2026-06-01"},
		{ID: "claude-opus-4-8", Backend: BackendClaudeAgent, Label: "Claude Agent · Opus 4.8", ContextWindow: 1_000_000},
	}
	adapters := []AdapterStatus{
		{Backend: string(BackendCodexCLI), Type: "cli", ModelDetails: []ModelDef{
			{ID: "gpt-5.6-sol", Name: "GPT-5.6-Sol", Reasoning: true, ReleaseDate: "2026-07-09", SupportedEfforts: []api.Effort{api.EffortLow, api.EffortMax}},
		}},
		{Backend: string(BackendAnthropic), Type: "api", ModelDetails: []ModelDef{
			{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Reasoning: true, ReleaseDate: "2026-06-29"},
		}},
		// gemini-cli has no menu backend; its models must not leak into the menu.
		{Backend: string(BackendGeminiCLI), Type: "cli", ModelDetails: []ModelDef{
			{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash"},
		}},
	}

	merged := mergeLiveCatalog(static, adapters, liveCatalogOptions{})

	// A live codex model becomes one codex-agent entry keyed by its exact id.
	sol := findModel(t, merged, "gpt-5.6-sol")
	if sol.Backend != BackendCodexAgent {
		t.Errorf("sol backend = %q, want codex-agent", sol.Backend)
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

	for _, m := range merged {
		if m.ID == "googleai/gemini-3.5-flash" || m.Backend == BackendGeminiCLI {
			t.Errorf("gemini-cli model leaked into the menu: %+v", m)
		}
	}
}

func TestLiveCatalogInfoAppliesPerProviderConfigured(t *testing.T) {
	prevProbe := adapterProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe = prevProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""
	adapterProbe = func(AuthProbe) ([]AdapterStatus, error) {
		return []AdapterStatus{
			{Backend: string(BackendAnthropic), Type: "api", ModelDetails: []ModelDef{
				{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Reasoning: true},
			}},
			{Backend: string(BackendOpenAI), Type: "api", ModelDetails: []ModelDef{
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
