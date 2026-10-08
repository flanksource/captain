package aichat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

func (s *Service) registerRuntimeProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/chat/runtime-presets/resolve", s.handleResolveRuntimePreset)
	mux.HandleFunc("POST /api/chat/runtime-profiles/resolve", s.handleResolveRuntimeProfile)
}

func (s *Service) handleResolveRuntimePreset(w http.ResponseWriter, request *http.Request) {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input api.RuntimePresetResolveRequest
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, fmt.Sprintf("decode runtime presets: %v", err), http.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		http.Error(w, "decode runtime presets: request must contain one JSON object", http.StatusBadRequest)
		return
	}
	resolved, err := api.ResolveRuntimePresets(input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	response, err := s.resolveRuntimePresetTools(request, resolved)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := writeJSON(w, http.StatusOK, response); err != nil {
		serviceLog.Errorf("write runtime preset resolution: %v", err)
	}
}

func (s *Service) handleResolveRuntimeProfile(w http.ResponseWriter, _ *http.Request) {
	serviceLog.Warnf("%s", api.RuntimeProfileDeprecationWarning)
	w.Header().Set("Warning", `299 Captain "`+api.RuntimeProfileDeprecationWarning+`"`)
	response := api.RuntimeProfileResolveResponse{Resolved: api.ResolvedSpec{Warnings: []string{api.RuntimeProfileDeprecationWarning}}}
	if err := writeJSON(w, http.StatusOK, response); err != nil {
		serviceLog.Errorf("write deprecated runtime profile resolution: %v", err)
	}
}

func (s *Service) resolveRuntimePresetTools(
	request *http.Request,
	resolved api.ResolvedSpec,
) (api.RuntimePresetResolveResponse, error) {
	set, err := s.loadTools(request.Context())
	if err != nil {
		return api.RuntimePresetResolveResponse{}, err
	}
	options := s.resolveOptions(resolved.Spec.ToolPreferences, resolved.Spec.ToolPolicy)
	permissions, err := aitools.ResolveToolPermissions(set.Definitions, options)
	if err != nil {
		return api.RuntimePresetResolveResponse{}, err
	}
	support := make(map[string]api.Support, len(permissions))
	capabilities := api.PermissionCapabilitiesFor(api.RuntimeOf(resolved.Spec.Provider, resolved.Spec.Mode))
	for name, policy := range permissions {
		support[name] = capabilities.ToolPolicySupport(api.ProvenanceCaller, policy)
	}
	return api.RuntimePresetResolveResponse{
		Resolved: resolved, Tools: set.Catalog, Permissions: permissions,
		PermissionSupport: support, EffectivePolicy: options.EffectivePolicy(),
	}, nil
}
