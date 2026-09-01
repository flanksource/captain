package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// The adapter probe and its logic tests live in pkg/ai (pkg/ai/adapters_test.go).
// These cover the CLI-only surface: the whoami command and its Pretty() renderer.

func TestWhoamiPrettyKeylessCmuxDoesNotRequestAPIKey(t *testing.T) {
	got := (WhoamiResult{
		Adapters: []AdapterStatus{{
			Provider:      ai.Anthropic.Name,
			Mode:          string(ai.ModeCmux),
			Type:          "cli",
			BinaryMissing: "claude",
		}},
	}).Pretty().String()

	if strings.Contains(got, "ANTHROPIC_API_KEY") || strings.Contains(got, "OPENAI_API_KEY") {
		t.Fatalf("keyless cmux pretty output should not request provider API keys: %q", got)
	}
	if !strings.Contains(got, "not configured") || !strings.Contains(got, "claude not in PATH") {
		t.Fatalf("pretty output should still explain local readiness, got %q", got)
	}
}

func TestWhoamiPrettyRendersModelListItems(t *testing.T) {
	adapter := AdapterStatus{
		Provider:      ai.OpenAI.Name,
		Mode:          string(ai.ModeAPI),
		Type:          "api",
		Authenticated: true,
		AuthMethod:    "OPENAI_API_KEY (env)",
		ModelCount:    6,
		Models:        []string{"m6", "m5", "m4", "m3", "m2", "m1"},
		ModelDetails: []ai.ModelDef{
			{ID: "m6", ReleaseDate: "2026-06-01"},
			{ID: "m5", ReleaseDate: "2026-05-01"},
			{ID: "m4", ReleaseDate: "2026-04-01"},
			{ID: "m3", ReleaseDate: "2026-03-01"},
			{ID: "m2", ReleaseDate: "2026-02-01"},
			{ID: "m1", ReleaseDate: "2026-01-01"},
		},
	}

	got := (WhoamiResult{
		Adapters:    []AdapterStatus{adapter},
		sampleLimit: 5,
		showModels:  true,
	}).Pretty().String()

	for _, want := range []string{
		"6 models",
		"- m6 (2026-06-01)",
		"- m2 (2026-02-01)",
		"... (+1 more)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Pretty() = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "m6, m5") {
		t.Errorf("Pretty() still renders a comma-separated model line: %q", got)
	}
	if strings.Contains(got, "- m1") {
		t.Errorf("Pretty() ignored sample limit: %q", got)
	}
}

// TestRunWhoami_NoModelsCoversEveryRuntime asserts the command lists exactly one
// adapter per provider×mode cell without making any network calls when
// --models=false.
func TestRunWhoami_NoModelsCoversEveryRuntime(t *testing.T) {
	res, err := RunWhoami(WhoamiOptions{Models: false})
	if err != nil {
		t.Fatalf("RunWhoami: %v", err)
	}
	r, ok := res.(WhoamiResult)
	if !ok {
		t.Fatalf("RunWhoami returned %T, want WhoamiResult", res)
	}
	if len(r.Adapters) != len(ai.AllRuntimes()) {
		t.Fatalf("got %d adapters, want %d", len(r.Adapters), len(ai.AllRuntimes()))
	}
	for _, a := range r.Adapters {
		if a.ModelCount != 0 || len(a.Models) != 0 {
			t.Errorf("adapter %s %s has models with --models=false: %+v", a.Provider, a.Mode, a)
		}
	}
}

func TestRunWhoami_RejectsUnknownAxes(t *testing.T) {
	if _, err := RunWhoami(WhoamiOptions{Mode: "bogus", Models: false}); err == nil {
		t.Fatal("expected error for unknown --mode")
	}
	if _, err := RunWhoami(WhoamiOptions{Provider: "bogus", Models: false}); err == nil {
		t.Fatal("expected error for unknown --provider")
	}
}

func TestRunWhoamiIncludesProviderDefaults(t *testing.T) {
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
	if err := captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
		DefaultProvider: "openai",
		Providers: map[string]captainconfig.ProviderDefaults{
			"openai": {Mode: "agent", Model: "gpt-5.6-sol", ReasoningEffort: "high"},
		},
	}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	result, err := RunWhoami(WhoamiOptions{Models: false})
	if err != nil {
		t.Fatalf("RunWhoami: %v", err)
	}
	got := result.(WhoamiResult)
	if got.DefaultProvider != "openai" || got.ProviderDefaults["openai"].Mode != "agent" {
		t.Fatalf("whoami defaults = %+v", got)
	}
}

// TestRunWhoamiAnnotatesDisabledAdapters covers the one surface that keeps
// disabled entries instead of dropping them: hiding a card would leave the user
// no way to switch it back on.
func TestRunWhoamiAnnotatesDisabledAdapters(t *testing.T) {
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	api.SetDisabled(api.DisabledSet{})
	t.Cleanup(func() {
		captainconfig.SetPathForTesting("")
		api.SetDisabled(api.DisabledSet{})
	})
	if err := captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
		Disabled: captainconfig.DisabledSelections{Modes: []string{"cmux"}, Efforts: []string{"ultra"}},
	}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	result, err := RunWhoami(WhoamiOptions{Models: false})
	if err != nil {
		t.Fatalf("RunWhoami: %v", err)
	}
	got := result.(WhoamiResult)

	if len(got.Adapters) != len(ai.AllRuntimes()) {
		t.Fatalf("got %d adapters, want all %d annotated rather than filtered", len(got.Adapters), len(ai.AllRuntimes()))
	}
	for _, adapter := range got.Adapters {
		if adapter.Provider == "" || adapter.Mode == "" {
			t.Errorf("adapter is missing its provider/mode axes: %+v", adapter)
			continue
		}
		wantDisabled := adapter.Mode == string(api.ModeCmux)
		if adapter.Disabled != wantDisabled {
			t.Errorf("adapter %s %s disabled = %v, want %v", adapter.Provider, adapter.Mode, adapter.Disabled, wantDisabled)
		}
		if wantDisabled && adapter.DisabledReason != "mode cmux" {
			t.Errorf("adapter %s %s reason = %q, want %q", adapter.Provider, adapter.Mode, adapter.DisabledReason, "mode cmux")
		}
	}
	if !reflect.DeepEqual(got.Disabled.Modes, []string{"cmux"}) {
		t.Errorf("whoami disabled set = %+v, want the saved modes", got.Disabled)
	}
}

// TestRunWhoamiServesTheRuntimeCatalogAnnotated keeps the page's runtime picker
// off a second hardcoded family list: it renders from this, and — like the
// adapter cards — sees disabled entries annotated rather than missing.
func TestRunWhoamiServesTheRuntimeCatalogAnnotated(t *testing.T) {
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	api.SetDisabled(api.DisabledSet{})
	t.Cleanup(func() {
		captainconfig.SetPathForTesting("")
		api.SetDisabled(api.DisabledSet{})
	})
	if err := captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
		Disabled: captainconfig.DisabledSelections{Modes: []string{"cmux"}},
	}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	result, err := RunWhoami(WhoamiOptions{Models: false})
	if err != nil {
		t.Fatalf("RunWhoami: %v", err)
	}
	runtimes := result.(WhoamiResult).Runtimes

	seen := map[string]bool{}
	for _, family := range runtimes {
		for _, mode := range family.Modes {
			seen[family.Provider+":"+mode.Mode] = true
			wantDisabled := mode.Mode == string(api.ModeCmux)
			if mode.Disabled != wantDisabled {
				t.Errorf("%s %s disabled = %v, want %v", family.Provider, mode.Mode, mode.Disabled, wantDisabled)
			}
			if wantDisabled && mode.DisabledReason != "mode cmux" {
				t.Errorf("%s %s reason = %q, want %q", family.Provider, mode.Mode, mode.DisabledReason, "mode cmux")
			}
		}
	}
	if len(seen) != len(api.AllRuntimes()) {
		t.Errorf("runtimes cover %d cells, want all %d annotated rather than filtered", len(seen), len(api.AllRuntimes()))
	}
}

// TestRunWhoamiServesEveryAxisUniverse keeps the page from hardcoding the enums
// a second time: the toggles are rendered from what this reports.
func TestRunWhoamiServesEveryAxisUniverse(t *testing.T) {
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })

	result, err := RunWhoami(WhoamiOptions{Models: false})
	if err != nil {
		t.Fatalf("RunWhoami: %v", err)
	}
	axes := result.(WhoamiResult).Axes

	if len(axes.Modes) != len(api.AllRuntimeModes()) {
		t.Errorf("axes.Modes = %v, want every runtime mode", axes.Modes)
	}
	if len(axes.Efforts) != len(api.AllEfforts()) {
		t.Errorf("axes.Efforts = %v, want every effort tier", axes.Efforts)
	}
	if !reflect.DeepEqual(axes.Providers, []string{"anthropic", "openai", "google", "deepseek"}) {
		t.Errorf("axes.Providers = %v", axes.Providers)
	}
}
