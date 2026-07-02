package cli

import (
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
	st := AdapterStatus{Backend: string(ai.BackendOpenAI)}
	setModels(&st, []ai.ModelDef{
		{ID: "gpt-old-unknown", Backend: ai.BackendOpenAI},
		{ID: "gpt-4o", Backend: ai.BackendOpenAI, ReleaseDate: "2026-07-01"},
		{ID: "gpt-new", Backend: ai.BackendOpenAI, ReleaseDate: "2026-06-01"},
		{ID: "gpt-mid", Backend: ai.BackendOpenAI, ReleaseDate: "2026-05-15"},
	})

	want := []string{"gpt-new", "gpt-mid", "gpt-old-unknown"}
	if st.ModelCount != len(want) {
		t.Fatalf("ModelCount = %d, want %d (%+v)", st.ModelCount, len(want), st)
	}
	for i, w := range want {
		if st.Models[i] != w {
			t.Errorf("Models[%d] = %q, want %q", i, st.Models[i], w)
		}
	}
}

func TestSetModelsKeepsCurrentCLIModelSlugs(t *testing.T) {
	st := AdapterStatus{Backend: string(ai.BackendCodexCLI)}
	setModels(&st, []ai.ModelDef{
		{ID: "gpt-5-codex", Backend: ai.BackendCodexCLI, ReleaseDate: "2025-08-07"},
	})

	if st.ModelCount != 1 || len(st.Models) != 1 || st.Models[0] != "gpt-5-codex" {
		t.Fatalf("codex CLI model should not be blacklisted: %+v", st)
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
		modelDetails: []ai.ModelDef{
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
