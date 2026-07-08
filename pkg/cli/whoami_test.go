package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
)

func TestMaskKey(t *testing.T) {
	cases := map[string]string{
		"sk-ant-api03-ABCDEFGH": "sk-a…EFGH",
		"AIzaSyD-xyz123":        "AIza…z123",
		"short":                 "****",
		"":                      "****",
		"12345678":              "****",
	}
	for in, want := range cases {
		if got := maskKey(in); got != want {
			t.Errorf("maskKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeProbe builds an authProbe whose env, PATH, and filesystem are fully
// controlled so resolveAdapter can be exercised without touching the host.
func fakeProbe(env map[string]string, binaries map[string]string, files map[string]bool, home string) authProbe {
	return authProbe{
		getenv: func(k string) string { return env[k] },
		lookPath: func(b string) (string, error) {
			if p, ok := binaries[b]; ok {
				return p, nil
			}
			return "", os.ErrNotExist
		},
		fileExists: func(p string) bool { return files[p] },
		home:       home,
	}
}

func TestResolveAdapter_APIKeyFromEnv(t *testing.T) {
	st := resolveAdapter(ai.BackendAnthropic, fakeProbe(
		map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api03-SECRETKEY"},
		nil, nil, "/home/u"))

	if st.Type != "api" {
		t.Errorf("Type = %q, want api", st.Type)
	}
	if !st.Authenticated || !st.Ready() {
		t.Errorf("expected authenticated+ready, got %+v", st)
	}
	if st.AuthMethod != "ANTHROPIC_API_KEY (env)" {
		t.Errorf("AuthMethod = %q", st.AuthMethod)
	}
	if st.AuthDetail != "sk-a…TKEY" {
		t.Errorf("AuthDetail = %q (key should be masked, never printed in full)", st.AuthDetail)
	}
}

func TestResolveAdapter_APINotConfigured(t *testing.T) {
	st := resolveAdapter(ai.BackendOpenAI, fakeProbe(nil, nil, nil, "/home/u"))
	if st.Authenticated || st.Ready() {
		t.Errorf("expected unauthenticated, got %+v", st)
	}
}

func TestResolveAdapter_CLILoginFile(t *testing.T) {
	home := "/home/u"
	authFile := filepath.Join(home, ".codex", "auth.json")
	st := resolveAdapter(ai.BackendCodexCLI, fakeProbe(
		nil,
		map[string]string{"codex": "/usr/local/bin/codex"},
		map[string]bool{authFile: true},
		home))

	if st.Type != "cli" {
		t.Errorf("Type = %q, want cli", st.Type)
	}
	if st.Binary != "/usr/local/bin/codex" {
		t.Errorf("Binary = %q", st.Binary)
	}
	if !st.Authenticated || !st.Ready() {
		t.Errorf("expected authenticated+ready via login file, got %+v", st)
	}
	if st.AuthMethod != "codex login" || st.AuthDetail != authFile {
		t.Errorf("AuthMethod=%q AuthDetail=%q", st.AuthMethod, st.AuthDetail)
	}
}

func TestResolveAdapter_CmuxUsesLocalLoginWithoutAPIKey(t *testing.T) {
	home := "/home/u"
	claudeAuth := filepath.Join(home, ".claude.json")
	codexAuth := filepath.Join(home, ".codex", "auth.json")

	claude := resolveAdapter(ai.BackendClaudeCmux, fakeProbe(
		nil,
		map[string]string{"claude": "/usr/local/bin/claude"},
		map[string]bool{claudeAuth: true},
		home))
	if claude.Type != "cli" || !claude.Ready() {
		t.Fatalf("claude-cmux should be ready through local claude login, got %+v", claude)
	}
	if claude.AuthMethod != "claude login" || claude.AuthDetail != claudeAuth {
		t.Fatalf("claude-cmux auth = %q %q", claude.AuthMethod, claude.AuthDetail)
	}

	codex := resolveAdapter(ai.BackendCodexCmux, fakeProbe(
		nil,
		map[string]string{"codex": "/usr/local/bin/codex"},
		map[string]bool{codexAuth: true},
		home))
	if codex.Type != "cli" || !codex.Ready() {
		t.Fatalf("codex-cmux should be ready through local codex login, got %+v", codex)
	}
	if codex.AuthMethod != "codex login" || codex.AuthDetail != codexAuth {
		t.Fatalf("codex-cmux auth = %q %q", codex.AuthMethod, codex.AuthDetail)
	}
}

func TestResolveAdapter_CLIBinaryMissingNotReady(t *testing.T) {
	home := "/home/u"
	st := resolveAdapter(ai.BackendGeminiCLI, fakeProbe(
		map[string]string{"GEMINI_API_KEY": "AIzaSyD-aaaaaaaa"}, // authenticated...
		nil, // ...but no gemini binary
		nil, home))

	if !st.Authenticated {
		t.Fatalf("expected authenticated via env key, got %+v", st)
	}
	if st.Binary != "" || st.BinaryMissing != "gemini" {
		t.Errorf("expected BinaryMissing=gemini, got Binary=%q BinaryMissing=%q", st.Binary, st.BinaryMissing)
	}
	if st.Ready() {
		t.Error("a CLI adapter with no binary in PATH must not be Ready")
	}
}

func TestResolveAdapter_EnvKeyPreferredOverLogin(t *testing.T) {
	home := "/home/u"
	st := resolveAdapter(ai.BackendClaudeAgent, fakeProbe(
		map[string]string{"ANTHROPIC_API_KEY": "sk-ant-PREFERREDKEY"},
		map[string]string{"claude": "/usr/local/bin/claude"},
		map[string]bool{filepath.Join(home, ".claude.json"): true},
		home))

	if st.AuthMethod != "ANTHROPIC_API_KEY (env)" {
		t.Errorf("env key should win over login file, got AuthMethod=%q", st.AuthMethod)
	}
}

func TestProbeAdaptersUsesLiveProviderModelsForClaudeCmux(t *testing.T) {
	prev := resolveModelRows
	resolveModelRows = func(_ context.Context, opts ai.ResolveOptions) ([]ai.ResolvedModel, error) {
		if opts.Backend != ai.BackendAnthropic || !opts.UseTokens {
			t.Fatalf("resolve opts = %+v, want anthropic live token resolve", opts)
		}
		return []ai.ResolvedModel{
			{Model: ai.Model{ID: "anthropic/claude-sonnet-5", Backend: ai.BackendAnthropic, Label: "Claude Sonnet 5", ReleaseDate: "2026-05-20"}, Live: true},
			{Model: ai.Model{ID: "claude-sonnet-4-6", Backend: ai.BackendAnthropic, Label: "Claude Sonnet 4.6", ReleaseDate: "2026-05-01"}, Live: true},
			{Model: ai.Model{ID: "claude-opus-4-8", Backend: ai.BackendAnthropic, Label: "Claude Opus 4.8", ReleaseDate: "2026-04-15"}, Live: true},
			{Model: ai.Model{ID: "anthropic/claude-sonnet-static", Backend: ai.BackendAnthropic, Label: "Static Sonnet"}, Live: false},
		}, nil
	}
	t.Cleanup(func() { resolveModelRows = prev })

	adapters, err := ProbeAdapters(WhoamiOptions{Backend: string(ai.BackendClaudeCmux), Models: true}, fakeProbe(
		map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"},
		map[string]string{"claude": "/usr/local/bin/claude"},
		nil,
		"/home/u",
	))
	if err != nil {
		t.Fatalf("ProbeAdapters: %v", err)
	}
	if len(adapters) != 1 {
		t.Fatalf("adapter count = %d, want 1", len(adapters))
	}
	got := adapters[0].Models
	for _, want := range []string{"claude-sonnet-5", "claude-sonnet-4-6", "claude-opus-4-8"} {
		if !stringSliceContains(got, want) {
			t.Fatalf("models = %v, want exact runtime model %q from live provider model list", got, want)
		}
	}
	for _, rejected := range []string{"sonnet-5", "sonnet-4-6", "opus-4-8", "claude-agent-sonnet", "claude-agent-opus", "claude-sonnet-static"} {
		if stringSliceContains(got, rejected) {
			t.Fatalf("models = %v, should not include alias/static id %q after adapter normalization", got, rejected)
		}
	}
}

func TestProbeAdaptersFiltersNoisyOpenAIModelsForCodexCmux(t *testing.T) {
	prev := resolveModelRows
	resolveModelRows = func(_ context.Context, opts ai.ResolveOptions) ([]ai.ResolvedModel, error) {
		if opts.Backend != ai.BackendOpenAI || !opts.UseTokens {
			t.Fatalf("resolve opts = %+v, want openai live token resolve", opts)
		}
		return []ai.ResolvedModel{
			{Model: ai.Model{ID: "openai/gpt-5.5", Backend: ai.BackendOpenAI, Label: "GPT-5.5", ReleaseDate: "2026-06-01"}, Live: true},
			{Model: ai.Model{ID: "openai/gpt-5.4", Backend: ai.BackendOpenAI, Label: "GPT-5.4", ReleaseDate: "2026-05-15"}, Live: true},
			{Model: ai.Model{ID: "openai/gpt-realtime-2.1", Backend: ai.BackendOpenAI, Label: "Realtime"}, Live: true},
			{Model: ai.Model{ID: "openai/gpt-image-2", Backend: ai.BackendOpenAI, Label: "Image"}, Live: true},
			{Model: ai.Model{ID: "openai/gpt-audio-1.5", Backend: ai.BackendOpenAI, Label: "Audio"}, Live: true},
			{Model: ai.Model{ID: "openai/gpt-5.3-codex", Backend: ai.BackendOpenAI, Label: "Codex"}, Live: true},
			{Model: ai.Model{ID: "openai/gpt-5.3-chat-latest", Backend: ai.BackendOpenAI, Label: "Chat latest"}, Live: true},
			{Model: ai.Model{ID: "openai/gpt-5.5-pro", Backend: ai.BackendOpenAI, Label: "Pro"}, Live: true},
			{Model: ai.Model{ID: "openai/o4-mini", Backend: ai.BackendOpenAI, Label: "O4 mini"}, Live: true},
			{Model: ai.Model{ID: "openai/sora-2", Backend: ai.BackendOpenAI, Label: "Sora"}, Live: true},
		}, nil
	}
	t.Cleanup(func() { resolveModelRows = prev })

	adapters, err := ProbeAdapters(WhoamiOptions{Backend: string(ai.BackendCodexCmux), Models: true}, fakeProbe(
		map[string]string{"OPENAI_API_KEY": "sk-test"},
		map[string]string{"codex": "/usr/local/bin/codex"},
		nil,
		"/home/u",
	))
	if err != nil {
		t.Fatalf("ProbeAdapters: %v", err)
	}
	if len(adapters) != 1 {
		t.Fatalf("adapter count = %d, want 1", len(adapters))
	}
	got := adapters[0].Models
	for _, want := range []string{"gpt-5.5", "gpt-5.4"} {
		if !stringSliceContains(got, want) {
			t.Fatalf("models = %v, want primary OpenAI model %q", got, want)
		}
	}
	for _, hidden := range []string{"gpt-realtime-2.1", "gpt-image-2", "gpt-audio-1.5", "gpt-5.3-codex", "gpt-5.3-chat-latest", "gpt-5.5-pro", "o4-mini", "sora-2"} {
		if stringSliceContains(got, hidden) {
			t.Fatalf("models = %v, should hide noisy OpenAI model %q", got, hidden)
		}
	}
}

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

func TestSetModelsFiltersAndSortsByReleaseDate(t *testing.T) {
	st := AdapterStatus{Backend: string(ai.BackendAnthropic)}
	setModels(&st, []ai.ModelDef{
		{ID: "claude-sonnet-5", Backend: ai.BackendAnthropic, ReleaseDate: "2026-06-01"},
		{ID: "claude-sonnet-4-6", Backend: ai.BackendAnthropic, ReleaseDate: "2026-05-01"},
		{ID: "claude-sonnet-4-5", Backend: ai.BackendAnthropic, ReleaseDate: "2026-04-01"},
		{ID: "claude-sonnet-4-4", Backend: ai.BackendAnthropic, ReleaseDate: "2026-03-01"},
		{ID: "claude-haiku-4-5", Backend: ai.BackendAnthropic, ReleaseDate: "2025-10-15"},
		{ID: "claude-3-5-sonnet-20241022", Backend: ai.BackendAnthropic, ReleaseDate: "2024-10-22"},
	})

	want := []string{"claude-sonnet-5", "claude-sonnet-4-6", "claude-sonnet-4-5", "claude-haiku-4-5"}
	if st.ModelCount != len(want) {
		t.Fatalf("ModelCount = %d, want %d (%+v)", st.ModelCount, len(want), st)
	}
	for i, w := range want {
		if st.Models[i] != w {
			t.Errorf("Models[%d] = %q, want %q", i, st.Models[i], w)
		}
	}
}

func TestSetModelsKeepsGeminiProFamily(t *testing.T) {
	st := AdapterStatus{Backend: string(ai.BackendGemini)}
	setModels(&st, []ai.ModelDef{
		{ID: "gemini-3.5-flash", Backend: ai.BackendGemini, ReleaseDate: "2026-06-10"},
		{ID: "gemini-3.0-pro", Backend: ai.BackendGemini},
		{ID: "gemini-2.5-pro", Backend: ai.BackendGemini, ReleaseDate: "2025-06-17"},
		{ID: "gemini-2.0-flash", Backend: ai.BackendGemini, ReleaseDate: "2025-02-05"},
	})

	want := []string{"gemini-3.5-flash", "gemini-3.0-pro", "gemini-2.5-pro"}
	if st.ModelCount != len(want) {
		t.Fatalf("ModelCount = %d, want %d (%+v)", st.ModelCount, len(want), st)
	}
	for i, w := range want {
		if st.Models[i] != w {
			t.Errorf("Models[%d] = %q, want %q", i, st.Models[i], w)
		}
	}
}

func TestSetModelsHidesCodexCodeVariantsForCLI(t *testing.T) {
	st := AdapterStatus{Backend: string(ai.BackendCodexCLI)}
	setModels(&st, []ai.ModelDef{
		{ID: "gpt-5-codex", Backend: ai.BackendCodexCLI, ReleaseDate: "2025-08-07"},
	})

	if st.ModelCount != 0 || len(st.Models) != 0 {
		t.Fatalf("codex code variant should be hidden for CLI: %+v", st)
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

func stringSliceContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
