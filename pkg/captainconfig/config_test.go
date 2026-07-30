package captainconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api/registry"
)

func withTempPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".captain.yaml")
	SetPathForTesting(path)
	t.Cleanup(func() { SetPathForTesting("") })
	return path
}

func TestLoad_MissingFileReturnsZero(t *testing.T) {
	withTempPath(t)
	cfg, exists, err := Load()
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if exists {
		t.Errorf("Load() exists = true, want false for missing file")
	}
	if !reflect.DeepEqual(cfg, Config{}) {
		t.Errorf("Load() cfg = %+v, want zero", cfg)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := withTempPath(t)
	want := Config{
		AI: AIDefaults{
			DefaultProvider: "anthropic",
			Providers: map[string]ProviderDefaults{
				"anthropic": {Agent: "claude-agent", Model: "claude-sonnet-4-6", ReasoningEffort: "medium"},
			},
			BudgetUSD:   2.5,
			MaxTokens:   8192,
			Temperature: 0.2,
			Timeout:     "180s",
			NoCache:     true,
			NoMCP:       true,
			NoHooks:     false,
			NoMemory:    true,
		},
		Prompts: PromptDefaults{
			Dirs: []string{"/repo/prompts"},
			SchemaRepair: SchemaRepairDefaults{
				Model:   "gpt-5",
				Backend: "openai",
				Prompt:  "/repo/prompts/json-repair.prompt",
			},
		},
	}
	if err := Save(want); err != nil {
		t.Fatalf("Save() err = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("saved file mode = %o, want 0644", mode)
	}

	got, exists, err := Load()
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if !exists {
		t.Fatalf("Load() exists = false after Save")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got  = %+v\n want = %+v", got, want)
	}
}

func TestLegacyDefaultsProjectIntoActiveProviderAndMigrateOnSave(t *testing.T) {
	path := withTempPath(t)
	legacy := []byte("ai:\n  backend: codex-agent\n  model: gpt-5.6-sol\n  reasoningEffort: high\n  maxTokens: 4096\n")
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("seed legacy config: %v", err)
	}
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load() legacy config: %v", err)
	}
	if got := cfg.AI.ActiveProvider(); got != "openai" {
		t.Fatalf("ActiveProvider() = %q, want openai", got)
	}
	if got := cfg.AI.Provider("openai"); !reflect.DeepEqual(got, ProviderDefaults{
		Agent: "codex-agent", Model: "gpt-5.6-sol", ReasoningEffort: "high",
	}) {
		t.Fatalf("Provider(openai) = %+v", got)
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save() migrated config: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "backend:") || strings.Contains(text, "\n  model:") || !strings.Contains(text, "defaultProvider: openai") {
		t.Fatalf("legacy fields were not migrated:\n%s", text)
	}
}

func TestLegacyDefaultsStayWithTheirProviderWhenDefaultProviderChanges(t *testing.T) {
	cfg := Config{AI: AIDefaults{
		DefaultProvider: "gemini",
		Backend:         "codex-agent",
		Model:           "gpt-5.6-sol",
		ReasoningEffort: "high",
	}}

	normalized := normalize(cfg)
	if got := normalized.AI.Providers["openai"]; got.Agent != "codex-agent" || got.Model != "gpt-5.6-sol" || got.ReasoningEffort != "high" {
		t.Fatalf("openai legacy defaults = %+v", got)
	}
	if got := normalized.AI.Providers["gemini"]; got != (ProviderDefaults{}) {
		t.Fatalf("gemini inherited openai legacy defaults: %+v", got)
	}
	if normalized.AI.DefaultProvider != "gemini" {
		t.Fatalf("default provider = %q", normalized.AI.DefaultProvider)
	}
}

func TestUpdatePreservesUnrelatedConfiguration(t *testing.T) {
	withTempPath(t)
	wantPrompt := PromptDefaults{Dirs: []string{"/repo/prompts"}}
	if err := Save(Config{Prompts: wantPrompt}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := Update(func(cfg *Config) error {
		cfg.AI.DefaultProvider = "gemini"
		cfg.AI.Providers = map[string]ProviderDefaults{
			"gemini": {Agent: "gemini-cli", Model: "gemini-3.5-flash"},
		}
		return nil
	}); err != nil {
		t.Fatalf("Update(): %v", err)
	}
	got, _, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if !reflect.DeepEqual(got.Prompts, wantPrompt) || got.AI.DefaultProvider != "gemini" {
		t.Fatalf("updated config = %+v", got)
	}
}

func TestDisabledSelections_RoundTrip(t *testing.T) {
	path := withTempPath(t)
	want := DisabledSelections{
		Modes:     []string{"cmux"},
		Providers: []string{"deepseek"},
		Backends:  []string{"gemini-cli"},
		Models:    []string{"gemini/veo-3.1-generate-preview"},
		Efforts:   []string{"ultra"},
	}
	if err := Save(Config{AI: AIDefaults{DefaultProvider: "anthropic", Disabled: want}}); err != nil {
		t.Fatalf("Save() err = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "disabled:") {
		t.Fatalf("saved config has no disabled block:\n%s", data)
	}

	got, _, err := Load()
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if !reflect.DeepEqual(got.AI.Disabled, want) {
		t.Errorf("disabled round-trip:\n got  = %+v\n want = %+v", got.AI.Disabled, want)
	}
}

func TestDisabledSelections_OmittedWhenEmpty(t *testing.T) {
	path := withTempPath(t)
	if err := Save(Config{AI: AIDefaults{DefaultProvider: "anthropic"}}); err != nil {
		t.Fatalf("Save() err = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "disabled:") {
		t.Errorf("empty opt-out set was written to the file:\n%s", data)
	}
}

func TestActiveProvider_SkipsADisabledProvider(t *testing.T) {
	// Every selection route into anthropic is exercised: the explicit default, the
	// legacy backend/model projection, and the hardcoded anthropic fallback.
	for name, cfg := range map[string]AIDefaults{
		"explicit default": {DefaultProvider: "anthropic"},
		"legacy backend":   {Backend: "claude-agent"},
		"legacy model":     {Model: "claude-sonnet-5"},
		"implicit default": {},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.Disabled = DisabledSelections{Providers: []string{"anthropic"}}
			got := registry.Backend(cfg.ActiveProvider())
			if got.Provider() == registry.AnthropicProvider {
				t.Fatalf("ActiveProvider() = %q, want a provider that is not disabled", got)
			}
			if got.Mode() != registry.ModeAPI || got.Provider() != got {
				t.Fatalf("ActiveProvider() = %q, want an API provider root", got)
			}
		})
	}
}

func TestApplyToRegistry_InstallsTheSetProcessWide(t *testing.T) {
	t.Cleanup(func() { registry.SetDisabled(registry.DisabledSet{}) })
	Config{AI: AIDefaults{Disabled: DisabledSelections{Modes: []string{"cmux"}}}}.ApplyToRegistry()

	if !registry.Disabled().Mode(registry.ModeCmux) {
		t.Fatal("ApplyToRegistry() did not install the opt-out set")
	}
}

func TestLoad_MalformedYAMLReturnsError(t *testing.T) {
	path := withTempPath(t)
	if err := os.WriteFile(path, []byte("ai: [not, a, mapping"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	_, _, err := Load()
	if err == nil {
		t.Fatal("Load() err = nil, want parse error for malformed YAML")
	}
}

func TestSave_AtomicLeavesNoTempFile(t *testing.T) {
	path := withTempPath(t)
	if err := Save(Config{AI: AIDefaults{Model: "x"}}); err != nil {
		t.Fatalf("Save() err = %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".captain.yaml" || name == ".captain.yaml.lock" {
			continue
		}
		t.Errorf("found stray file alongside config: %s", name)
	}
}
