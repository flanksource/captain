package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/credentials"
)

const providerTokenBodyLimit = 8 << 10

type providerTokenRequest struct {
	Token string `json:"token"`
}

type providerTokenResponse struct {
	Provider    string `json:"provider"`
	Valid       bool   `json:"valid"`
	Saved       bool   `json:"saved"`
	Source      string `json:"source"`
	MaskedToken string `json:"maskedToken"`
	ModelCount  int    `json:"modelCount"`
}

func registerProviderTokenHandlers(mux *http.ServeMux) {
	mux.Handle("PUT /api/captain/ai/providers/{provider}/token", handleProviderToken(false))
	mux.Handle("POST /api/captain/ai/providers/{provider}/token/test", handleProviderToken(true))
}

func handleProviderToken(testOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateLocalConfigurationRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		backend := ai.Backend(strings.TrimSpace(r.PathValue("provider")))
		if !configurableAPIBackend(backend) {
			http.Error(w, "provider must be one of: anthropic, openai, gemini, deepseek", http.StatusBadRequest)
			return
		}
		request, err := decodeProviderTokenRequest(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		token, source := strings.TrimSpace(request.Token), "candidate"
		if token == "" {
			if !testOnly {
				http.Error(w, "token cannot be empty", http.StatusBadRequest)
				return
			}
			resolved, err := ai.ResolveAPIKey(backend)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			token, source = resolved.Token, resolved.Source
			if token == "" {
				http.Error(w, fmt.Sprintf("no credential configured for %s", backend), http.StatusBadRequest)
				return
			}
		}

		models, err := configureTokenModels(r.Context(), backend, token)
		if err != nil {
			http.Error(w, fmt.Sprintf("validate %s credential: %v", backend, err), providerValidationStatus(err))
			return
		}
		if !testOnly {
			vault, err := credentials.DefaultVault()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := vault.Set(string(backend), token); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			source = credentials.SourceVault
		}
		writeConfigurationJSON(w, providerTokenResponse{
			Provider: string(backend), Valid: true, Saved: !testOnly,
			Source: source, MaskedToken: ai.MaskKey(token), ModelCount: len(models),
		})
	})
}

func validateLocalConfigurationRequest(r *http.Request) error {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("configuration changes are restricted to loopback clients")
	}
	requestHost := r.Host
	if parsed, _, err := net.SplitHostPort(requestHost); err == nil {
		requestHost = parsed
	}
	requestIP := net.ParseIP(requestHost)
	if !strings.EqualFold(requestHost, "localhost") && (requestIP == nil || !requestIP.IsLoopback()) {
		return fmt.Errorf("configuration changes require a loopback request host")
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
		return fmt.Errorf("configuration changes require a same-origin request")
	}
	return nil
}

func decodeProviderTokenRequest(w http.ResponseWriter, r *http.Request) (providerTokenRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return providerTokenRequest{}, fmt.Errorf("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, providerTokenBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request providerTokenRequest
	if err := decoder.Decode(&request); err != nil {
		return providerTokenRequest{}, fmt.Errorf("decode request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return providerTokenRequest{}, fmt.Errorf("request must contain one JSON object")
	}
	return request, nil
}

func providerValidationStatus(err error) int {
	var httpErr ai.ModelHTTPError
	if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
		return http.StatusUnprocessableEntity
	}
	return http.StatusBadGateway
}

func writeConfigurationJSON(w http.ResponseWriter, response any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
