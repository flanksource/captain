package ai

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

var adapterInputMediaTypes = map[api.Backend][]string{
	api.BackendGemini:      {"image/*", "audio/*", "video/*", "application/pdf"},
	api.BackendAnthropic:   {"image/*"},
	api.BackendOpenAI:      {"image/*"},
	api.BackendDeepSeek:    {},
	api.BackendCodexAgent:  {"image/*"},
	api.BackendCodexCLI:    {"image/*"},
	api.BackendClaudeAgent: {"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"},
	api.BackendClaudeCLI:   {},
	api.BackendGeminiCLI:   {},
	api.BackendClaudeCmux:  {},
	api.BackendCodexCmux:   {},
}

func AdapterInputMediaTypes(backend api.Backend) []string {
	return append([]string{}, adapterInputMediaTypes[backend]...)
}

func ValidateAttachmentCompatibility(models []api.Model, refs []api.AttachmentRef) error {
	if len(refs) == 0 {
		return nil
	}
	for _, model := range models {
		backend, err := model.ResolveBackend()
		if err != nil {
			return fmt.Errorf("resolve attachment backend for %s: %w", model.Name, err)
		}
		accepted := modelInputMediaTypes(backend, model.Name)
		for _, ref := range refs {
			if !mediaTypeAccepted(accepted, ref.MediaType) {
				return fmt.Errorf("%s:%s does not accept %s attachments", backend, model.Name, ref.MediaType)
			}
		}
	}
	return nil
}

func modelInputMediaTypes(backend api.Backend, model string) []string {
	if def, ok := RegistryModelDef(backend, model); ok {
		return def.InputMediaTypes
	}
	return AdapterInputMediaTypes(backend)
}

func clampInputMediaTypes(backend api.Backend, modelTypes []string) []string {
	adapterTypes := AdapterInputMediaTypes(backend)
	if len(adapterTypes) == 0 || len(modelTypes) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(modelTypes))
	for _, modelType := range modelTypes {
		modelType = normalizeInputMediaType(modelType)
		if modelType == "" {
			continue
		}
		for _, adapterType := range adapterTypes {
			mediaType, ok := intersectMediaTypes(modelType, adapterType)
			if ok && !slices.Contains(out, mediaType) {
				out = append(out, mediaType)
			}
		}
	}
	return out
}

func intersectMediaTypes(modelType, adapterType string) (string, bool) {
	if modelType == adapterType {
		return modelType, true
	}
	if strings.HasSuffix(modelType, "/*") && mediaTypeAccepted([]string{modelType}, adapterType) {
		return adapterType, true
	}
	if strings.HasSuffix(adapterType, "/*") && mediaTypeAccepted([]string{adapterType}, modelType) {
		return modelType, true
	}
	return "", false
}

func normalizeInputMediaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image", "image/*":
		return "image/*"
	case "audio", "audio/*":
		return "audio/*"
	case "video", "video/*":
		return "video/*"
	case "pdf", "application/pdf":
		return "application/pdf"
	case "text", "text/*":
		return ""
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func mediaTypeAccepted(accepted []string, mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	for _, pattern := range accepted {
		if pattern == mediaType || strings.HasSuffix(pattern, "/*") && strings.HasPrefix(mediaType, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}
