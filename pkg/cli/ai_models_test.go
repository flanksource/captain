package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
)

type aiModelsRewriteTransport struct {
	base  string
	inner http.RoundTripper
}

func (r aiModelsRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := http.NewRequest(req.Method, r.base+req.URL.Path, req.Body)
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

func withMockedProviders(t *testing.T, openai http.HandlerFunc, anthropic http.HandlerFunc) {
	t.Helper()
	mux := http.NewServeMux()
	if openai != nil {
		mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				openai(w, r)
				return
			}
			anthropic(w, r)
		})
	} else if anthropic != nil {
		mux.HandleFunc("/v1/models", anthropic)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	orig := http.DefaultClient.Transport
	http.DefaultClient.Transport = aiModelsRewriteTransport{base: srv.URL, inner: srv.Client().Transport}
	t.Cleanup(func() { http.DefaultClient.Transport = orig })
}

func TestRunAIModels_LiveOpenAIOnly(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")

	withMockedProviders(t,
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "gpt-5"},
					{"id": "gpt-5.1"},
				},
			})
		},
		nil,
	)

	got, err := RunAIModels(AIModelsOptions{Backend: "openai", Limit: 50})
	if err != nil {
		t.Fatalf("RunAIModels: %v", err)
	}
	res := got.(AIModelsResult)
	if res.Total != 2 {
		t.Fatalf("Total = %d, want 2", res.Total)
	}
	for _, row := range res.Rows {
		if row.Backend != "openai" {
			t.Errorf("backend = %q on %+v", row.Backend, row)
		}
	}
}

func TestRunAIModels_LiveErrorIsSurfaced(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")

	withMockedProviders(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		nil,
	)

	_, err := RunAIModels(AIModelsOptions{Backend: "openai", Limit: 10})
	if err == nil {
		t.Fatal("expected error to be surfaced (no static fallback)")
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("error message should mention backend, got %v", err)
	}
}

func TestRunAIModels_NoKeyIsSurfaced(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := RunAIModels(AIModelsOptions{Backend: "openai"})
	if err == nil {
		t.Fatal("expected error when key missing (no static fallback)")
	}
}

func TestRunAIModels_RejectsUnsupportedBackend(t *testing.T) {
	_, err := RunAIModels(AIModelsOptions{Backend: "gemini"})
	if err == nil {
		t.Fatal("expected error for unsupported backend")
	}
}

func TestIsLegacyModelID(t *testing.T) {
	cases := map[string]bool{
		// OpenAI legacy / non-chat
		"gpt-3.5-turbo":          true,
		"gpt-3.5-turbo-instruct": true,
		"gpt-4":                  true,
		"gpt-4-turbo":            true,
		"gpt-4.1":                true,
		"gpt-4o":                 true,
		"gpt-4o-mini":            true,
		"gpt-5-mini":             true,
		"gpt-5-nano":             true,
		"gpt-5-codex":            true,
		"gpt-5-pro":              true,
		"o1":                     true,
		"o1-pro":                 true,
		"o3":                     true,
		"o3-pro":                 true,
		"o3-mini":                true,
		"codex-mini-latest":      true,
		"dall-e-3":               true,
		"dall-e-2":               true,
		"whisper-1":              true,
		"tts-1":                  true,
		"text-embedding-3-small": true,
		"text-moderation-latest": true,
		"omni-moderation-latest": true,
		"babbage-002":            true,
		"davinci-002":            true,
		"chatgpt-4o-latest":      true,
		"computer-use-preview":   true,

		// Anthropic legacy
		"claude-3-opus-20240229":     true,
		"claude-3-5-sonnet-20241022": true,
		"claude-3-7-sonnet-latest":   true,
		"claude-2.1":                 true,
		"claude-instant-1.2":         true,
		"claude-sonnet-4-0":          true,
		"claude-opus-4-1":            true,

		// Gemini legacy
		"gemini-1.5-pro":   true,
		"gemini-1.5-flash": true,
		"gemini-2.0-flash": true,

		// Grok legacy (this is the case the user complained about)
		"grok-3":           true,
		"grok-3-mini":      true,
		"grok-code-fast-1": true,

		// Kept (current generation chat/reasoning):
		"gpt-5":                false,
		"gpt-5.1":              false,
		"gpt-5.2":              false,
		"gpt-5.3":              false,
		"o4-mini":              false,
		"claude-sonnet-4-5":    false,
		"claude-sonnet-4-6":    false,
		"claude-opus-4-5":      false,
		"claude-haiku-4-5":     false,
		"gemini-2.5-flash":     false,
		"gemini-2.5-pro":       false,
		"gemini-3-pro-preview": false,
		"grok-4":               false,
		"grok-4-fast":          false,
	}
	for id, want := range cases {
		if got := ai.IsLegacyModelID(id); got != want {
			t.Errorf("isLegacyModelID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestRunAIModels_HidesLegacyByDefault(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")

	withMockedProviders(t,
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "gpt-5"},
					{"id": "gpt-5.1"},
					{"id": "gpt-5-mini"},
					{"id": "gpt-5-codex"},
					{"id": "gpt-3.5-turbo"},
					{"id": "gpt-4"},
					{"id": "gpt-4o-mini"},
					{"id": "gpt-4.1"},
					{"id": "o1"},
					{"id": "o3-pro"},
					{"id": "dall-e-3"},
					{"id": "whisper-1"},
					{"id": "text-embedding-3-large"},
					{"id": "o4-mini"},
				},
			})
		},
		nil,
	)

	got, err := RunAIModels(AIModelsOptions{Backend: "openai", Limit: 50})
	if err != nil {
		t.Fatalf("RunAIModels: %v", err)
	}
	res := got.(AIModelsResult)

	want := []string{"gpt-5", "gpt-5.1", "o4-mini"}
	if len(res.Rows) != len(want) {
		t.Fatalf("rows = %d, want %d (%v)", len(res.Rows), len(want), res.Rows)
	}
	for i, w := range want {
		if res.Rows[i].Model != w {
			t.Errorf("rows[%d].Model = %q, want %q", i, res.Rows[i].Model, w)
		}
	}
}

func TestRunAIModels_FilterOverridesBlacklist(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")

	withMockedProviders(t,
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "gpt-5"},
					{"id": "gpt-3.5-turbo"},
					{"id": "gpt-4o-mini"},
				},
			})
		},
		nil,
	)

	got, err := RunAIModels(AIModelsOptions{Backend: "openai", Filter: "gpt-3.5", Limit: 10})
	if err != nil {
		t.Fatalf("RunAIModels: %v", err)
	}
	res := got.(AIModelsResult)
	if res.Total != 1 || res.Rows[0].Model != "gpt-3.5-turbo" {
		t.Errorf("explicit filter should override blacklist, got %+v", res)
	}
}

func TestRunAIModels_SortsByBackendThenModel(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "ant-test")

	withMockedProviders(t,
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "gpt-5.1"},
					{"id": "gpt-5"},
				},
			})
		},
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "claude-sonnet-4-5"},
					{"id": "claude-opus-4-5"},
				},
			})
		},
	)

	got, err := RunAIModels(AIModelsOptions{Limit: 50})
	if err != nil {
		t.Fatalf("RunAIModels: %v", err)
	}
	res := got.(AIModelsResult)
	wantOrder := []string{
		"anthropic|claude-opus-4-5",
		"anthropic|claude-sonnet-4-5",
		"openai|gpt-5",
		"openai|gpt-5.1",
	}
	if len(res.Rows) != len(wantOrder) {
		t.Fatalf("rows = %d, want %d (%+v)", len(res.Rows), len(wantOrder), res.Rows)
	}
	for i, want := range wantOrder {
		got := res.Rows[i].Backend + "|" + res.Rows[i].Model
		if got != want {
			t.Errorf("rows[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestRunAIModels_LimitTruncatesAfterSort(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")

	withMockedProviders(t,
		func(w http.ResponseWriter, _ *http.Request) {
			// Return alphabetically out-of-order so we can see the sort
			// happen before the limit cap. Only kept-by-blacklist ids
			// are used here so we are testing sort+limit, not the filter.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "gpt-5.3"},
					{"id": "gpt-5"},
					{"id": "gpt-5.1"},
					{"id": "gpt-5.2"},
				},
			})
		},
		nil,
	)

	got, err := RunAIModels(AIModelsOptions{Backend: "openai", Limit: 2})
	if err != nil {
		t.Fatalf("RunAIModels: %v", err)
	}
	res := got.(AIModelsResult)
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Rows))
	}
	if res.Rows[0].Model != "gpt-5" || res.Rows[1].Model != "gpt-5.1" {
		t.Errorf("limit cut off the wrong rows; got %+v", res.Rows)
	}
}

func TestRunAIModels_FilterAppliesToLiveResults(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")

	withMockedProviders(t,
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "gpt-5"},
					{"id": "gpt-5-mini"},
					{"id": "o3"},
				},
			})
		},
		nil,
	)

	got, err := RunAIModels(AIModelsOptions{Backend: "openai", Filter: "mini", Limit: 50})
	if err != nil {
		t.Fatalf("RunAIModels: %v", err)
	}
	res := got.(AIModelsResult)
	if res.Total != 1 || res.Rows[0].Model != "gpt-5-mini" {
		t.Errorf("filter not applied, got %+v", res)
	}
}
