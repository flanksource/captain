package ai

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credentials"
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
		if got := MaskKey(in); got != want {
			t.Errorf("MaskKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeProbe builds an AuthProbe whose env, PATH, and filesystem are fully
// controlled so resolveAdapter can be exercised without touching the host.
func fakeProbe(env map[string]string, binaries map[string]string, files map[string]bool, home string) AuthProbe {
	return AuthProbe{
		Getenv: func(k string) string { return env[k] },
		LookPath: func(b string) (string, error) {
			if p, ok := binaries[b]; ok {
				return p, nil
			}
			return "", os.ErrNotExist
		},
		FileExists: func(p string) bool { return files[p] },
		Home:       home,
	}
}

func TestResolveAdapter_APIKeyFromEnv(t *testing.T) {
	st := resolveAdapter(BackendAnthropic, fakeProbe(
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
	st := resolveAdapter(BackendOpenAI, fakeProbe(nil, nil, nil, "/home/u"))
	if st.Authenticated || st.Ready() {
		t.Errorf("expected unauthenticated, got %+v", st)
	}
}

func TestOSAuthProbeUsesVaultCredential(t *testing.T) {
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	for _, name := range api.AuthEnvVars(BackendOpenAI) {
		t.Setenv(name, "")
	}
	vault, err := credentials.DefaultVault()
	if err != nil {
		t.Fatalf("DefaultVault: %v", err)
	}
	if err := vault.Set("openai", "vault-openai-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	adapters, err := ProbeAdapters(WhoamiOptions{Backend: "openai"}, OSAuthProbe())
	if err != nil {
		t.Fatalf("ProbeAdapters: %v", err)
	}
	if len(adapters) != 1 {
		t.Fatalf("adapters = %+v", adapters)
	}
	if got := adapters[0]; !got.Authenticated || got.AuthMethod != "Captain vault" || got.AuthDetail != "vaul…cret" {
		t.Fatalf("adapter = %+v, want masked Captain vault credential", got)
	}
}

func TestResolveAdapter_CLILoginFile(t *testing.T) {
	home := "/home/u"
	authFile := filepath.Join(home, ".codex", "auth.json")
	st := resolveAdapter(BackendCodexCLI, fakeProbe(
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

	claude := resolveAdapter(BackendClaudeCmux, fakeProbe(
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

	codex := resolveAdapter(BackendCodexCmux, fakeProbe(
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
	st := resolveAdapter(BackendGeminiCLI, fakeProbe(
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
	st := resolveAdapter(BackendClaudeAgent, fakeProbe(
		map[string]string{"ANTHROPIC_API_KEY": "sk-ant-PREFERREDKEY"},
		map[string]string{"claude": "/usr/local/bin/claude"},
		map[string]bool{filepath.Join(home, ".claude.json"): true},
		home))

	if st.AuthMethod != "ANTHROPIC_API_KEY (env)" {
		t.Errorf("env key should win over login file, got AuthMethod=%q", st.AuthMethod)
	}
}

func TestProbeAdaptersUsesRegistryModelsForClaudeCmuxRegardlessOfAPIKey(t *testing.T) {
	prev := resolveModelRows
	resolveModelRows = func(_ context.Context, opts ResolveOptions) ([]ResolvedModel, error) {
		t.Fatalf("local Claude adapter should not resolve provider API models: %+v", opts)
		return nil, nil
	}
	t.Cleanup(func() { resolveModelRows = prev })

	var withoutKey []string
	for _, env := range []map[string]string{nil, {"ANTHROPIC_API_KEY": "sk-ant-test"}} {
		adapters, err := ProbeAdapters(WhoamiOptions{Backend: string(BackendClaudeCmux), Models: true}, fakeProbe(
			env,
			map[string]string{"claude": "/usr/local/bin/claude"},
			nil,
			"/home/u",
		))
		if err != nil {
			t.Fatalf("ProbeAdapters: %v", err)
		}
		if len(adapters) != 1 || len(adapters[0].Models) == 0 {
			t.Fatalf("adapters = %+v, want one adapter with registry models", adapters)
		}
		if !stringSliceContains(adapters[0].Models, "claude-fable-5") {
			t.Fatalf("models = %v, want preferred Fable model", adapters[0].Models)
		}
		var fable *ModelDef
		for i := range adapters[0].ModelDetails {
			if adapters[0].ModelDetails[i].ID == "claude-fable-5" {
				fable = &adapters[0].ModelDetails[i]
				break
			}
		}
		if fable == nil || !fable.CapabilitiesKnown || !fable.Reasoning || fable.Temperature || len(fable.SupportedEfforts) != 5 {
			t.Fatalf("fable model details = %+v", fable)
		}
		if withoutKey == nil {
			withoutKey = append([]string(nil), adapters[0].Models...)
			continue
		}
		if strings.Join(adapters[0].Models, "\x00") != strings.Join(withoutKey, "\x00") {
			t.Fatalf("models with key = %v, without key = %v", adapters[0].Models, withoutKey)
		}
	}
}

func TestProbeAdaptersFiltersNoisyOpenAIModelsForDirectAPI(t *testing.T) {
	prev := resolveModelRows
	resolveModelRows = func(_ context.Context, opts ResolveOptions) ([]ResolvedModel, error) {
		if opts.Backend != BackendOpenAI || !opts.UseTokens {
			t.Fatalf("resolve opts = %+v, want openai live token resolve", opts)
		}
		return []ResolvedModel{
			{Model: Model{ID: "openai/gpt-5.5", Backend: BackendOpenAI, Label: "GPT-5.5", ReleaseDate: "2026-06-01"}, Live: true},
			{Model: Model{ID: "openai/gpt-5.4", Backend: BackendOpenAI, Label: "GPT-5.4", ReleaseDate: "2026-05-15"}, Live: true},
			{Model: Model{ID: "openai/gpt-realtime-2.1", Backend: BackendOpenAI, Label: "Realtime"}, Live: true},
			{Model: Model{ID: "openai/gpt-image-2", Backend: BackendOpenAI, Label: "Image"}, Live: true},
			{Model: Model{ID: "openai/gpt-audio-1.5", Backend: BackendOpenAI, Label: "Audio"}, Live: true},
			{Model: Model{ID: "openai/gpt-5.3-codex", Backend: BackendOpenAI, Label: "Codex"}, Live: true},
			{Model: Model{ID: "openai/gpt-5.3-chat-latest", Backend: BackendOpenAI, Label: "Chat latest"}, Live: true},
			{Model: Model{ID: "openai/gpt-5.5-pro", Backend: BackendOpenAI, Label: "Pro"}, Live: true},
			{Model: Model{ID: "openai/o4-mini", Backend: BackendOpenAI, Label: "O4 mini"}, Live: true},
			{Model: Model{ID: "openai/sora-2", Backend: BackendOpenAI, Label: "Sora"}, Live: true},
		}, nil
	}
	t.Cleanup(func() { resolveModelRows = prev })

	adapters, err := ProbeAdapters(WhoamiOptions{Backend: string(BackendOpenAI), Models: true}, fakeProbe(
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
	for _, hidden := range []string{"gpt-realtime-2.1", "gpt-image-2", "gpt-audio-1.5", "gpt-5.3-chat-latest", "gpt-5.5-pro", "o4-mini", "sora-2"} {
		if stringSliceContains(got, hidden) {
			t.Fatalf("models = %v, should hide noisy OpenAI model %q", got, hidden)
		}
	}
}

func TestProbeAdaptersNoCacheBypassesPersistedModelCache(t *testing.T) {
	for _, tc := range []struct {
		name        string
		noCache     bool
		wantRefresh bool
	}{
		{name: "default reuses the persisted resolve", noCache: false, wantRefresh: false},
		{name: "no-cache re-resolves live", noCache: true, wantRefresh: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := resolveModelRows
			resolved := false
			resolveModelRows = func(_ context.Context, opts ResolveOptions) ([]ResolvedModel, error) {
				resolved = true
				if opts.Refresh != tc.wantRefresh {
					t.Fatalf("ResolveOptions.Refresh = %v, want %v", opts.Refresh, tc.wantRefresh)
				}
				return []ResolvedModel{
					{Model: Model{ID: "anthropic/claude-opus-5", Backend: BackendAnthropic, Label: "Opus 5", ReleaseDate: "2026-05-01"}, Live: true},
				}, nil
			}
			t.Cleanup(func() { resolveModelRows = prev })

			adapters, err := ProbeAdapters(
				WhoamiOptions{Backend: string(BackendAnthropic), Models: true, NoCache: tc.noCache},
				fakeProbe(map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}, nil, nil, "/home/u"),
			)
			if err != nil {
				t.Fatalf("ProbeAdapters: %v", err)
			}
			if !resolved {
				t.Fatal("expected the model resolver to run for an authenticated API backend")
			}
			if len(adapters) != 1 || !stringSliceContains(adapters[0].Models, "claude-opus-5") {
				t.Fatalf("adapters = %+v, want the resolved model", adapters)
			}
		})
	}
}

func TestProbeAdaptersUsesCodexDebugModelsOnceRegardlessOfAPIKey(t *testing.T) {
	probe := fakeProbe(map[string]string{"OPENAI_API_KEY": "sk-test"}, map[string]string{"codex": "/usr/local/bin/codex"}, nil, "/home/u")
	calls := 0
	probe.CodexModels = func(_ context.Context, binary string) ([]ModelDef, error) {
		calls++
		if binary != "/usr/local/bin/codex" {
			t.Fatalf("binary = %q", binary)
		}
		return []ModelDef{
			{ID: "gpt-5.6-sol", Name: "GPT-5.6-Sol", Priority: 1, DefaultEffort: api.EffortLow, SupportedEfforts: []api.Effort{api.EffortLow, api.EffortHigh, api.EffortUltra}},
			{ID: "gpt-5.6-luna", Name: "GPT-5.6-Luna", Priority: 3, DefaultEffort: api.EffortMedium, SupportedEfforts: []api.Effort{api.EffortLow, api.EffortHigh, api.EffortMax}},
		}, nil
	}
	adapters, err := ProbeAdapters(WhoamiOptions{Models: true}, probe)
	if err != nil {
		t.Fatalf("ProbeAdapters: %v", err)
	}
	if calls != 1 {
		t.Fatalf("codex debug calls = %d, want 1", calls)
	}
	for _, backend := range []Backend{BackendCodexCLI, BackendCodexAgent, BackendCodexCmux} {
		adapter := findAdapter(t, adapters, backend)
		if len(adapter.Models) != 2 || adapter.Models[0] != "gpt-5.6-sol" {
			t.Fatalf("%s models = %v", backend, adapter.Models)
		}
		if adapter.ModelDetails[0].DefaultEffort != api.EffortLow {
			t.Fatalf("%s details = %+v", backend, adapter.ModelDetails[0])
		}
	}
}

func TestFetchCodexModelsUsesDebugWithAPIKey(t *testing.T) {
	probe := fakeProbe(map[string]string{"OPENAI_API_KEY": "sk-test"}, map[string]string{"codex": "/usr/local/bin/codex"}, nil, "/home/u")
	calls := 0
	probe.CodexModels = func(_ context.Context, binary string) ([]ModelDef, error) {
		calls++
		if binary != "/usr/local/bin/codex" {
			t.Fatalf("binary = %q", binary)
		}
		return []ModelDef{{ID: "gpt-5.6-sol"}}, nil
	}
	got := fetchCodexModels([]Backend{BackendCodexAgent}, probe)
	if got.err != nil || len(got.models) != 1 || calls != 1 {
		t.Fatalf("fetch = %+v calls = %d, want one discovered model", got, calls)
	}
}

func TestProbeAdaptersFallsBackToRegistryWhenCodexDebugFails(t *testing.T) {
	probe := fakeProbe(nil, map[string]string{"codex": "/usr/local/bin/codex"}, nil, "/home/u")
	probe.CodexModels = func(context.Context, string) ([]ModelDef, error) {
		return nil, errors.New("old codex")
	}
	adapters, err := ProbeAdapters(WhoamiOptions{Backend: string(BackendCodexCLI), Models: true}, probe)
	if err != nil {
		t.Fatalf("ProbeAdapters: %v", err)
	}
	if len(adapters) != 1 || !stringSliceContains(adapters[0].Models, "gpt-5.6-sol") {
		t.Fatalf("registry fallback models = %+v", adapters)
	}
}

func findAdapter(t *testing.T, adapters []AdapterStatus, backend Backend) AdapterStatus {
	t.Helper()
	for _, adapter := range adapters {
		if adapter.Backend == string(backend) {
			return adapter
		}
	}
	t.Fatalf("adapter %s not found", backend)
	return AdapterStatus{}
}

func TestSetModelsFiltersAndSortsByReleaseDate(t *testing.T) {
	st := AdapterStatus{Backend: string(BackendAnthropic)}
	setModels(&st, []ModelDef{
		{ID: "claude-sonnet-5", Backend: BackendAnthropic, ReleaseDate: "2026-06-01"},
		{ID: "claude-sonnet-4-6", Backend: BackendAnthropic, ReleaseDate: "2026-05-01"},
		{ID: "claude-sonnet-4-5", Backend: BackendAnthropic, ReleaseDate: "2026-04-01"},
		{ID: "claude-sonnet-4-4", Backend: BackendAnthropic, ReleaseDate: "2026-03-01"},
		{ID: "claude-haiku-4-5", Backend: BackendAnthropic, ReleaseDate: "2025-10-15"},
		{ID: "claude-3-5-sonnet-20241022", Backend: BackendAnthropic, ReleaseDate: "2024-10-22"},
	}, false)

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
	st := AdapterStatus{Backend: string(BackendGemini)}
	setModels(&st, []ModelDef{
		{ID: "gemini-3.5-flash", Backend: BackendGemini, ReleaseDate: "2026-06-10"},
		{ID: "gemini-3.0-pro", Backend: BackendGemini},
		{ID: "gemini-2.5-pro", Backend: BackendGemini, ReleaseDate: "2025-06-17"},
		{ID: "gemini-2.0-flash", Backend: BackendGemini, ReleaseDate: "2025-02-05"},
	}, false)

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
	st := AdapterStatus{Backend: string(BackendCodexCLI)}
	setModels(&st, []ModelDef{
		{ID: "gpt-5-codex", Backend: BackendCodexCLI, ReleaseDate: "2025-08-07"},
	}, false)

	if st.ModelCount != 0 || len(st.Models) != 0 {
		t.Fatalf("codex code variant should be hidden for CLI: %+v", st)
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
