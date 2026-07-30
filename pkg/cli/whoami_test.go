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
			Backend:       string(ai.BackendClaudeCmux),
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
		Backend:       string(ai.BackendOpenAI),
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

// TestRunWhoami_NoModelsCoversEveryBackend asserts the command lists exactly one
// adapter per backend without making any network calls when --models=false.
func TestRunWhoami_NoModelsCoversEveryBackend(t *testing.T) {
	res, err := RunWhoami(WhoamiOptions{Models: false})
	if err != nil {
		t.Fatalf("RunWhoami: %v", err)
	}
	r, ok := res.(WhoamiResult)
	if !ok {
		t.Fatalf("RunWhoami returned %T, want WhoamiResult", res)
	}
	if len(r.Adapters) != len(ai.AllBackends()) {
		t.Fatalf("got %d adapters, want %d", len(r.Adapters), len(ai.AllBackends()))
	}
	for _, a := range r.Adapters {
		if a.ModelCount != 0 || len(a.Models) != 0 {
			t.Errorf("adapter %s has models with --models=false: %+v", a.Backend, a)
		}
	}
}

func TestRunWhoami_RejectsUnknownBackend(t *testing.T) {
	if _, err := RunWhoami(WhoamiOptions{Backend: "bogus", Models: false}); err == nil {
		t.Fatal("expected error for unknown --backend")
	}
}

func TestRunWhoamiIncludesProviderDefaults(t *testing.T) {
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
	if err := captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
		DefaultProvider: "openai",
		Providers: map[string]captainconfig.ProviderDefaults{
			"openai": {Agent: "codex-agent", Model: "gpt-5.6-sol", ReasoningEffort: "high"},
		},
	}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	result, err := RunWhoami(WhoamiOptions{Models: false})
	if err != nil {
		t.Fatalf("RunWhoami: %v", err)
	}
	got := result.(WhoamiResult)
	if got.DefaultProvider != "openai" || got.ProviderDefaults["openai"].Agent != "codex-agent" {
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

	if len(got.Adapters) != len(ai.AllBackends()) {
		t.Fatalf("got %d adapters, want all %d annotated rather than filtered", len(got.Adapters), len(ai.AllBackends()))
	}
	for _, adapter := range got.Adapters {
		wantDisabled := ai.Backend(adapter.Backend).Mode() == api.ModeCmux
		if adapter.Disabled != wantDisabled {
			t.Errorf("adapter %s disabled = %v, want %v", adapter.Backend, adapter.Disabled, wantDisabled)
		}
		if wantDisabled && adapter.DisabledReason != "mode cmux" {
			t.Errorf("adapter %s reason = %q, want %q", adapter.Backend, adapter.DisabledReason, "mode cmux")
		}
		if adapter.Provider == "" || adapter.Mode == "" {
			t.Errorf("adapter %s is missing its provider/mode axes: %+v", adapter.Backend, adapter)
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
			seen[mode.Backend] = true
			wantDisabled := mode.Mode == string(api.ModeCmux)
			if mode.Disabled != wantDisabled {
				t.Errorf("%s disabled = %v, want %v", mode.Backend, mode.Disabled, wantDisabled)
			}
			if wantDisabled && mode.DisabledReason != "mode cmux" {
				t.Errorf("%s reason = %q, want %q", mode.Backend, mode.DisabledReason, "mode cmux")
			}
		}
	}
	if len(seen) != len(api.AllBackends()) {
		t.Errorf("runtimes cover %d backends, want all %d annotated rather than filtered", len(seen), len(api.AllBackends()))
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
	if !reflect.DeepEqual(axes.Providers, []string{"anthropic", "openai", "gemini", "deepseek"}) {
		t.Errorf("axes.Providers = %v", axes.Providers)
	}
}
