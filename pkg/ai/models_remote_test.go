package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withTestServer redirects http.DefaultClient.Do to the given test server by
// rewriting requests to the mocked URL. We restore the original transport on
// cleanup so other tests are unaffected.
func withTestServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}
	t.Cleanup(func() { http.DefaultClient.Transport = orig })
}

type rewriteTransport struct {
	base  string
	inner http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Strip scheme+host but preserve path AND query so the Gemini fetcher's
	// `?key=…` survives the rewrite.
	target := r.base + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	u, err := http.NewRequest(req.Method, target, req.Body)
	if err != nil {
		return nil, err
	}
	u.Header = req.Header
	inner := r.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(u)
}

func TestFetchOpenAIModels_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/models") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-5"},
				{"id": "gpt-5-mini"},
				{"id": "gpt-99-future"},
			},
		})
	}))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := FetchOpenAIModels(context.Background(), "sk-test")
	if err != nil {
		t.Fatalf("FetchOpenAIModels: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	for _, m := range got {
		if m.Backend != BackendOpenAI {
			t.Errorf("Backend = %q on %+v", m.Backend, m)
		}
		if m.Name == "" || m.ID == "" {
			t.Errorf("missing fields: %+v", m)
		}
	}
}

func TestFetchOpenAIModels_EmptyKey(t *testing.T) {
	if _, err := FetchOpenAIModels(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty api key")
	}
}

func TestFetchOpenAIModels_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()
	withTestServer(t, srv)

	if _, err := FetchOpenAIModels(context.Background(), "sk-bad"); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestFetchAnthropicModels_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "ant-test" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "claude-sonnet-4-6", "display_name": "Claude Sonnet 4.6"},
				{"id": "claude-future-1", "display_name": "Claude Future 1"},
			},
		})
	}))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := FetchAnthropicModels(context.Background(), "ant-test")
	if err != nil {
		t.Fatalf("FetchAnthropicModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Name != "Claude Sonnet 4.6" {
		t.Errorf("display_name not surfaced: %+v", got[0])
	}
	for _, m := range got {
		if m.Backend != BackendAnthropic {
			t.Errorf("Backend = %q on %+v", m.Backend, m)
		}
	}
}

func TestListModels_ErrorsOnMissingKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	for _, b := range []Backend{BackendOpenAI, BackendAnthropic, BackendGemini, BackendClaudeCLI, BackendCodexCLI, BackendGeminiCLI} {
		_, err := ListModels(context.Background(), b)
		if err == nil {
			t.Errorf("backend=%s: expected error when no API key set", b)
		}
	}
}

func TestListModels_ErrorsOnHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	withTestServer(t, srv)

	t.Setenv("OPENAI_API_KEY", "sk-test")
	if _, err := ListModels(context.Background(), BackendOpenAI); err == nil {
		t.Fatal("expected HTTP 500 to be surfaced as an error (no static fallback)")
	}
}

func TestListModels_CodexCLIRoutesThroughOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("OpenAI auth header not forwarded: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "gpt-5"}, {"id": "gpt-5-codex"}},
		})
	}))
	defer srv.Close()
	withTestServer(t, srv)

	t.Setenv("OPENAI_API_KEY", "sk-test")
	got, err := ListModels(context.Background(), BackendCodexCLI)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for _, m := range got {
		if m.Backend != BackendCodexCLI {
			t.Errorf("expected re-tagged Backend=codex-cli, got %q on %+v", m.Backend, m)
		}
	}
}

func TestListModels_SortsAlphabetically(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "gpt-5.1"}, {"id": "gpt-5"}, {"id": "gpt-5.2"}},
		})
	}))
	defer srv.Close()
	withTestServer(t, srv)

	t.Setenv("OPENAI_API_KEY", "sk-test")
	got, err := ListModels(context.Background(), BackendOpenAI)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	wantOrder := []string{"gpt-5", "gpt-5.1", "gpt-5.2"}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, w)
		}
	}
}

func TestFetchGeminiModels_StripsModelsPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Gemini sends the key as a query param, not a header.
		if got := r.URL.Query().Get("key"); got != "g-test" {
			t.Errorf("key= = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "models/gemini-2.5-flash", "display_name": "Gemini 2.5 Flash"},
				{"name": "models/gemini-2.5-pro", "display_name": "Gemini 2.5 Pro"},
			},
		})
	}))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := FetchGeminiModels(context.Background(), "g-test")
	if err != nil {
		t.Fatalf("FetchGeminiModels: %v", err)
	}
	if len(got) != 2 || got[0].ID != "gemini-2.5-flash" || got[0].Name != "Gemini 2.5 Flash" {
		t.Errorf("unexpected: %+v", got)
	}
	for _, m := range got {
		if m.Backend != BackendGemini {
			t.Errorf("Backend = %q on %+v", m.Backend, m)
		}
	}
}
