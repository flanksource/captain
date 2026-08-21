package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/credentials"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Strip scheme+host while preserving request metadata for provider checks.
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
				{"id": "gpt-5", "created": time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC).Unix()},
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
	if got[0].ReleaseDate != "2026-03-02" {
		t.Errorf("ReleaseDate = %q, want parsed OpenAI created date", got[0].ReleaseDate)
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
				{"id": "claude-sonnet-4-6", "display_name": "Claude Sonnet 4.6", "created_at": "2026-04-05T12:30:00Z"},
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
	if got[0].ReleaseDate != "2026-04-05" {
		t.Errorf("ReleaseDate = %q, want parsed Anthropic created_at date", got[0].ReleaseDate)
	}
	for _, m := range got {
		if m.Backend != BackendAnthropic {
			t.Errorf("Backend = %q on %+v", m.Backend, m)
		}
	}
}

func TestFetchDeepSeekModels_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ds-test" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "deepseek-chat"},
				{"id": "deepseek-reasoner"},
			},
		})
	}))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := FetchDeepSeekModels(context.Background(), "ds-test")
	if err != nil {
		t.Fatalf("FetchDeepSeekModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	for _, m := range got {
		if m.Backend != BackendDeepSeek {
			t.Errorf("Backend = %q on %+v", m.Backend, m)
		}
		if m.ID == "" || m.Name == "" {
			t.Errorf("missing fields: %+v", m)
		}
	}
}

func TestFetchDeepSeekModels_EmptyKey(t *testing.T) {
	if _, err := FetchDeepSeekModels(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty api key")
	}
}

func TestListModels_ErrorsOnMissingKey(t *testing.T) {
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")

	for _, b := range []Backend{BackendOpenAI, BackendAnthropic, BackendGemini, BackendDeepSeek} {
		_, err := ListModels(context.Background(), b)
		if err == nil {
			t.Errorf("backend=%s: expected error when no API key set", b)
		}
	}
}

// TestListModels_RejectsNonAPIBackends pins that non-API backends have no live
// listing path: they authenticate internally and enumerate from the static
// catalog (pkg/cli), so ListModels must fail loud rather than require an API key.
func TestListModels_RejectsNonAPIBackends(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "ant-test")
	t.Setenv("GEMINI_API_KEY", "g-test")

	for _, b := range []Backend{
		BackendClaudeCLI,
		BackendClaudeAgent,
		BackendCodexCLI,
		BackendGeminiCLI,
		BackendClaudeCmux,
		BackendCodexCmux,
	} {
		if _, err := ListModels(context.Background(), b); err == nil {
			t.Errorf("backend=%s: expected error (no live listing for non-API backends)", b)
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

func TestListModelsWithAPIKeyAndURLUsesExactResolvedEndpointAndCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.RequestURI(); got != "/tenant/v1/models?account=private" {
			t.Errorf("request URI = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer caller-token-b" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "private-model-b"}},
		})
	}))
	defer srv.Close()

	models, err := ListModelsWithAPIKeyAndURL(
		context.Background(), BackendOpenAI, "caller-token-b", srv.URL+"/tenant/v1?account=private",
	)
	if err != nil {
		t.Fatalf("ListModelsWithAPIKeyAndURL: %v", err)
	}
	if len(models) != 1 || models[0].ID != "private-model-b" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListModelsWithAPIKeyAndURLRedactsEndpointFromTransportErrors(t *testing.T) {
	original := http.DefaultClient.Transport
	transportErr := errors.New("https://tenant.example/v1/models?access_token=query-secret: transport unavailable")
	http.DefaultClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})
	t.Cleanup(func() { http.DefaultClient.Transport = original })

	_, err := ListModelsWithAPIKeyAndURL(
		context.Background(), BackendOpenAI, "caller-token", "https://tenant.example/v1?access_token=query-secret",
	)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "query-secret") || strings.Contains(err.Error(), "tenant.example") {
		t.Fatalf("transport error exposed the custom endpoint: %v", err)
	}
	if !errors.Is(err, transportErr) {
		t.Fatalf("transport error does not preserve its cause: %v", err)
	}
}

func TestListModelsWithAPIKeyAndURLDoesNotFollowRedirects(t *testing.T) {
	redirected := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		redirected <- r.Clone(r.Context())
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	_, err := ListModelsWithAPIKeyAndURL(
		context.Background(), BackendAnthropic, "caller-token", source.URL+"?access_token=query-secret",
	)
	var httpErr ModelHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect error = %v, want HTTP %d", err, http.StatusTemporaryRedirect)
	}
	select {
	case request := <-redirected:
		t.Fatalf("redirect target received credential-bearing request: %+v", request)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestFetchGeminiModels_StripsModelsPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "g-test" {
			t.Errorf("x-goog-api-key = %q", got)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("API key must not be present in URL query: %q", r.URL.RawQuery)
		}
		// Both ids must be preferred registry models: the release-date fallback
		// reads Catalog(), which only carries preferred entries.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "models/gemini-3.6-flash", "display_name": "Gemini 3.6 Flash"},
				{"name": "models/gemini-3.5-flash", "display_name": "Gemini 3.5 Flash"},
			},
		})
	}))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := FetchGeminiModels(context.Background(), "g-test")
	if err != nil {
		t.Fatalf("FetchGeminiModels: %v", err)
	}
	if len(got) != 2 || got[0].ID != "gemini-3.6-flash" || got[0].Name != "Gemini 3.6 Flash" {
		t.Errorf("unexpected: %+v", got)
	}
	if got[1].ID != "gemini-3.5-flash" || got[1].ReleaseDate == "" {
		t.Errorf("expected Gemini catalog release-date fallback, got %+v", got)
	}
	for _, m := range got {
		if m.Backend != BackendGemini {
			t.Errorf("Backend = %q on %+v", m.Backend, m)
		}
	}
}
