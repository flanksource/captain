package cli

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
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
