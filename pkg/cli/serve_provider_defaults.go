package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

type providerDefaultsRequest struct {
	Mode   string `json:"mode"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type providerDefaultsResponse struct {
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Active   bool   `json:"active"`
}

type defaultProviderRequest struct {
	Provider string `json:"provider"`
}

func registerProviderDefaultsHandlers(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/captain/ai/providers/{provider}/defaults", handleProviderDefaults)
	mux.HandleFunc("PUT /api/captain/ai/default-provider", handleDefaultProvider)
}

func handleProviderDefaults(w http.ResponseWriter, r *http.Request) {
	if err := validateLocalConfigurationRequest(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	provider, known := api.ProviderByName(strings.TrimSpace(r.PathValue("provider")))
	if !known {
		http.Error(w, "provider must be one of: "+api.ProviderList(), http.StatusBadRequest)
		return
	}
	var request providerDefaultsRequest
	if err := decodeConfigurationRequest(w, r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defaults := ProviderDefaultView{
		Mode: strings.TrimSpace(request.Mode), Model: strings.TrimSpace(request.Model),
		Effort: strings.TrimSpace(request.Effort), Configured: true,
	}
	if err := validateProviderDefaults(r.Context(), provider, defaults); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	active := false
	if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		if err := cfg.AI.SetProvider(provider, captainconfig.ProviderDefaults{
			Mode: defaults.Mode, Model: defaults.Model, ReasoningEffort: defaults.Effort,
		}); err != nil {
			return err
		}
		active = cfg.AI.ActiveProvider() == provider.Name
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeConfigurationJSON(w, providerDefaultsResponse{
		Provider: provider.Name, Mode: defaults.Mode, Model: defaults.Model,
		Effort: defaults.Effort, Active: active,
	})
}

func handleDefaultProvider(w http.ResponseWriter, r *http.Request) {
	if err := validateLocalConfigurationRequest(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var request defaultProviderRequest
	if err := decodeConfigurationRequest(w, r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	provider, known := api.ProviderByName(strings.TrimSpace(request.Provider))
	if !known {
		http.Error(w, "provider must be one of: "+api.ProviderList(), http.StatusBadRequest)
		return
	}
	if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		cfg.AI.DefaultProvider = provider.Name
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeConfigurationJSON(w, defaultProviderRequest{Provider: provider.Name})
}

func decodeConfigurationRequest(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, providerTokenBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain one JSON object")
	}
	return nil
}
