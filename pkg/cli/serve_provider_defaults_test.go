package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/clicky/rpc"
)

func TestProviderDefaultsPutValidatesAndSaves(t *testing.T) {
	setupProviderDefaultsTest(t)
	previous := configureDefaultsModels
	configureDefaultsModels = func(_ context.Context, provider *api.ModelProvider, mode api.RuntimeMode) ([]ai.ModelDef, error) {
		if provider != api.OpenAI || mode != api.ModeAgent {
			t.Fatalf("runtime = %s %s", provider.Name, mode)
		}
		return []ai.ModelDef{{ID: "gpt-5.6-sol"}}, nil
	}
	t.Cleanup(func() { configureDefaultsModels = previous })

	response := serveProviderDefaultsRequest(t, "/api/captain/ai/providers/openai/defaults",
		`{"mode":"agent","model":"gpt-5.6-sol","effort":"high"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result providerDefaultsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Mode != "agent" || result.Model != "gpt-5.6-sol" || result.Effort != "high" {
		t.Fatalf("response = %+v", result)
	}
	config, _, err := captainconfig.Load()
	if err != nil || config.AI.Providers["openai"].Model != "gpt-5.6-sol" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestProviderDefaultsPutFailurePreservesExistingDefaults(t *testing.T) {
	setupProviderDefaultsTest(t)
	if err := captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
		Providers: map[string]captainconfig.ProviderDefaults{
			"openai": {Mode: "api", Model: "gpt-existing", ReasoningEffort: "medium"},
		},
	}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	previous := configureDefaultsModels
	configureDefaultsModels = func(context.Context, *api.ModelProvider, api.RuntimeMode) ([]ai.ModelDef, error) {
		return []ai.ModelDef{{ID: "gpt-valid"}}, nil
	}
	t.Cleanup(func() { configureDefaultsModels = previous })

	response := serveProviderDefaultsRequest(t, "/api/captain/ai/providers/openai/defaults",
		`{"mode":"api","model":"gpt-invalid","effort":"high"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	config, _, err := captainconfig.Load()
	if err != nil || config.AI.Providers["openai"].Model != "gpt-existing" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestDefaultProviderPutChangesOnlyActiveProvider(t *testing.T) {
	setupProviderDefaultsTest(t)
	if err := captainconfig.Save(captainconfig.Config{Prompts: captainconfig.PromptDefaults{Dirs: []string{"/repo/prompts"}}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	response := serveProviderDefaultsRequest(t, "/api/captain/ai/default-provider", `{"provider":"google"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	config, _, err := captainconfig.Load()
	if err != nil || config.AI.DefaultProvider != "google" || len(config.Prompts.Dirs) != 1 {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestProviderDefaultsOpenAPIPaths(t *testing.T) {
	spec := &rpc.OpenAPISpec{}
	addCaptainProviderDefaultsPaths(spec)
	if _, ok := spec.Paths["/api/captain/ai/providers/{provider}/defaults"]; !ok {
		t.Fatal("missing provider defaults OpenAPI path")
	}
	if _, ok := spec.Paths["/api/captain/ai/default-provider"]; !ok {
		t.Fatal("missing default provider OpenAPI path")
	}
}

func setupProviderDefaultsTest(t *testing.T) {
	t.Helper()
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
}

func serveProviderDefaultsRequest(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerProviderDefaultsHandlers(mux)
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:9020")
	request.Host = "localhost:9020"
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
