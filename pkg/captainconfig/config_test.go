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
				"anthropic": {Mode: "agent", Model: "claude-sonnet-4-6", ReasoningEffort: "medium"},
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
				Model:  "gpt-5",
				Mode:   "api",
				Prompt: "/repo/prompts/json-repair.prompt",
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

func TestUpdatePreservesUnrelatedConfiguration(t *testing.T) {
	withTempPath(t)
	wantPrompt := PromptDefaults{Dirs: []string{"/repo/prompts"}}
	if err := Save(Config{Prompts: wantPrompt}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := Update(func(cfg *Config) error {
		cfg.AI.DefaultProvider = "google"
		cfg.AI.Providers = map[string]ProviderDefaults{
			"google": {Mode: "cli", Model: "gemini-3.5-flash"},
		}
		return nil
	}); err != nil {
		t.Fatalf("Update(): %v", err)
	}
	got, _, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if !reflect.DeepEqual(got.Prompts, wantPrompt) || got.AI.DefaultProvider != "google" {
		t.Fatalf("updated config = %+v", got)
	}
}

func TestDisabledSelections_RoundTrip(t *testing.T) {
	path := withTempPath(t)
	want := DisabledSelections{
		Modes:     []string{"cmux"},
		Providers: []string{"deepseek"},
		Runtimes:  []DisabledRuntime{{Provider: "google", Mode: "cli"}},
		Models:    []string{"google/veo-3.1-generate-preview"},
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
	// Both routes into anthropic are exercised: the explicit default and the
	// hardcoded fallback.
	for name, cfg := range map[string]AIDefaults{
		"explicit default": {DefaultProvider: "anthropic"},
		"implicit default": {},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.Disabled = DisabledSelections{Providers: []string{"anthropic"}}
			got := cfg.ActiveProvider()
			if got == registry.Anthropic.Name {
				t.Fatalf("ActiveProvider() = %q, want a provider that is not disabled", got)
			}
			if _, ok := registry.ProviderByName(got); !ok {
				t.Fatalf("ActiveProvider() = %q, which names no provider", got)
			}
		})
	}
}

// A file written before a runtime became (model, mode) is rejected with a
// message naming the key and its replacement. It is never silently upgraded:
// every composite adapter id would have to be guessed back into a mode, and the
// user would then be running somewhere they never chose.
func TestRemovedKeysAreRejectedWithGuidance(t *testing.T) {
	cases := map[string]struct {
		yaml   string
		expect []string
	}{
		"legacy global backend": {
			yaml:   "ai:\n  backend: codex-agent\n  model: gpt-5.6-sol\n",
			expect: []string{"ai.backend", "ai.model", "ai.providers.<provider>.mode"},
		},
		"per-provider agent": {
			yaml:   "ai:\n  providers:\n    anthropic:\n      agent: claude-agent\n",
			expect: []string{"ai.providers.*.agent", "mode: api | agent | cli | cmux"},
		},
		"disabled backends list": {
			yaml:   "ai:\n  disabled:\n    backends: [claude-cmux]\n",
			expect: []string{"ai.disabled.backends", "ai.disabled.runtimes: [{provider: anthropic, mode: cmux}]"},
		},
		"schema repair backend": {
			yaml:   "prompts:\n  schemaRepair:\n    backend: openai\n",
			expect: []string{"prompts.schemaRepair.backend", "prompts.schemaRepair.mode"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := withTempPath(t)
			if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
				t.Fatalf("seed config: %v", err)
			}
			_, _, err := Load()
			if err == nil {
				t.Fatal("Load() accepted a removed key")
			}
			for _, want := range tc.expect {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q:\n%v", want, err)
				}
			}
		})
	}
}

// The keys that replaced them must not themselves trip the check — a `mode:` in
// the sandbox block or a provider literally named "model" would otherwise make
// the config unloadable.
func TestCurrentKeysLoadCleanly(t *testing.T) {
	path := withTempPath(t)
	current := "ai:\n" +
		"  defaultProvider: anthropic\n" +
		"  providers:\n    anthropic:\n      mode: agent\n      model: claude-opus-5\n" +
		"  disabled:\n    runtimes:\n      - provider: anthropic\n        mode: cmux\n" +
		"prompts:\n  schemaRepair:\n    mode: api\n    model: gpt-5\n"
	if err := os.WriteFile(path, []byte(current), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, exists, err := Load()
	if err != nil || !exists {
		t.Fatalf("Load() = %v, %v", exists, err)
	}
	if got := cfg.AI.Provider("anthropic"); got.Mode != "agent" || got.Model != "claude-opus-5" {
		t.Fatalf("anthropic defaults = %+v", got)
	}
	if got := cfg.AI.Disabled.Runtimes; len(got) != 1 || got[0].Provider != "anthropic" || got[0].Mode != "cmux" {
		t.Fatalf("disabled runtimes = %+v", got)
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
	if err := Save(Config{AI: AIDefaults{DefaultProvider: "anthropic"}}); err != nil {
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
