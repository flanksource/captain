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
	"github.com/flanksource/captain/pkg/credentials"
	"github.com/flanksource/clicky/rpc"
)

func TestProviderTokenPutValidatesAndSavesWithoutEchoingSecret(t *testing.T) {
	setupProviderTokenTest(t)
	const secret = "submitted-provider-secret"
	previous := configureTokenModels
	configureTokenModels = func(_ context.Context, backend ai.Backend, token string) ([]ai.ModelDef, error) {
		if backend != ai.BackendGemini || token != secret {
			t.Fatalf("validation got backend=%s token=%q", backend, token)
		}
		return []ai.ModelDef{{ID: "gemini-example"}}, nil
	}
	t.Cleanup(func() { configureTokenModels = previous })

	response := serveProviderTokenRequest(t, http.MethodPut, "/api/captain/ai/providers/gemini/token", `{"token":"`+secret+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatalf("response exposed token: %s", response.Body.String())
	}
	var result providerTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !result.Valid || !result.Saved || result.ModelCount != 1 || result.Source != credentials.SourceVault {
		t.Fatalf("response = %+v", result)
	}
	vault, _ := credentials.DefaultVault()
	values, err := vault.Load()
	if err != nil || values["gemini"] != secret {
		t.Fatalf("vault=%v err=%v", values, err)
	}
}

func TestProviderTokenPutRejectsCredentialWithoutReplacingOldToken(t *testing.T) {
	setupProviderTokenTest(t)
	vault, _ := credentials.DefaultVault()
	if err := vault.Set("openai", "existing-provider-secret"); err != nil {
		t.Fatalf("seed vault: %v", err)
	}
	previous := configureTokenModels
	configureTokenModels = func(context.Context, ai.Backend, string) ([]ai.ModelDef, error) {
		return nil, ai.ModelHTTPError{Backend: ai.BackendOpenAI, StatusCode: http.StatusUnauthorized}
	}
	t.Cleanup(func() { configureTokenModels = previous })

	response := serveProviderTokenRequest(t, http.MethodPut, "/api/captain/ai/providers/openai/token", `{"token":"invalid-provider-secret"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	values, err := vault.Load()
	if err != nil || values["openai"] != "existing-provider-secret" {
		t.Fatalf("vault=%v err=%v", values, err)
	}
}

func TestProviderTokenTestCurrentDoesNotWrite(t *testing.T) {
	setupProviderTokenTest(t)
	t.Setenv("ANTHROPIC_API_KEY", "environment-provider-secret")
	previous := configureTokenModels
	configureTokenModels = func(_ context.Context, _ ai.Backend, token string) ([]ai.ModelDef, error) {
		if token != "environment-provider-secret" {
			t.Fatalf("token=%q", token)
		}
		return []ai.ModelDef{{ID: "claude-example"}}, nil
	}
	t.Cleanup(func() { configureTokenModels = previous })

	response := serveProviderTokenRequest(t, http.MethodPost, "/api/captain/ai/providers/anthropic/token/test", `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result providerTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Saved || result.Source != credentials.SourceEnvironment {
		t.Fatalf("response = %+v", result)
	}
}

func TestProviderTokenEndpointsRejectRemoteAndCrossOriginRequests(t *testing.T) {
	setupProviderTokenTest(t)
	mux := http.NewServeMux()
	registerProviderTokenHandlers(mux)

	remote := httptest.NewRequest(http.MethodPost, "/api/captain/ai/providers/openai/token/test", strings.NewReader(`{}`))
	remote.Header.Set("Content-Type", "application/json")
	remote.RemoteAddr = "203.0.113.10:1234"
	remoteResponse := httptest.NewRecorder()
	mux.ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d", remoteResponse.Code)
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, "/api/captain/ai/providers/openai/token/test", strings.NewReader(`{}`))
	crossOrigin.Header.Set("Content-Type", "application/json")
	crossOrigin.Header.Set("Origin", "http://example.invalid")
	crossOrigin.Host = "localhost:9020"
	crossOrigin.RemoteAddr = "127.0.0.1:1234"
	crossOriginResponse := httptest.NewRecorder()
	mux.ServeHTTP(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", crossOriginResponse.Code)
	}

	rebound := httptest.NewRequest(http.MethodPost, "/api/captain/ai/providers/openai/token/test", strings.NewReader(`{}`))
	rebound.Header.Set("Content-Type", "application/json")
	rebound.Header.Set("Origin", "http://captain.example")
	rebound.Host = "captain.example"
	rebound.RemoteAddr = "127.0.0.1:1234"
	reboundResponse := httptest.NewRecorder()
	mux.ServeHTTP(reboundResponse, rebound)
	if reboundResponse.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host status=%d", reboundResponse.Code)
	}
}

func TestProviderTokenOpenAPIPaths(t *testing.T) {
	spec := &rpc.OpenAPISpec{}
	addCaptainProviderTokenPaths(spec)
	if _, ok := spec.Paths["/api/captain/ai/providers/{provider}/token"]; !ok {
		t.Fatal("missing token save OpenAPI path")
	}
	if _, ok := spec.Paths["/api/captain/ai/providers/{provider}/token/test"]; !ok {
		t.Fatal("missing token test OpenAPI path")
	}
}

func setupProviderTokenTest(t *testing.T) {
	t.Helper()
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "DEEPSEEK_API_KEY"} {
		t.Setenv(name, "")
	}
}

func serveProviderTokenRequest(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerProviderTokenHandlers(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:9020")
	req.Host = "localhost:9020"
	req.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	return response
}
