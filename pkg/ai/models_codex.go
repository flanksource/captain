package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

type codexDebugModelsResponse struct {
	Models []codexDebugModel `json:"models"`
}

type codexDebugModel struct {
	Slug                     string                     `json:"slug"`
	DisplayName              string                     `json:"display_name"`
	DefaultReasoningLevel    api.Effort                 `json:"default_reasoning_level"`
	SupportedReasoningLevels []codexDebugReasoningLevel `json:"supported_reasoning_levels"`
	Visibility               string                     `json:"visibility"`
	Priority                 int                        `json:"priority"`
}

type codexDebugReasoningLevel struct {
	Effort api.Effort `json:"effort"`
}

// FetchCodexDebugModels reads the model catalog exposed by the installed Codex
// binary. The command works from Codex's bundled catalog without an API key.
func FetchCodexDebugModels(ctx context.Context, binary string) ([]ModelDef, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return nil, fmt.Errorf("codex binary is required")
	}
	ctx, cancel := context.WithTimeout(ctx, remoteModelsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "debug", "models").Output()
	if err != nil {
		return nil, fmt.Errorf("codex debug models: %w", err)
	}
	return ParseCodexDebugModels(out)
}

// ParseCodexDebugModels converts Codex's raw catalog into Captain model rows.
func ParseCodexDebugModels(data []byte) ([]ModelDef, error) {
	var response codexDebugModelsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("codex debug models decode: %w", err)
	}
	models := make([]ModelDef, 0, len(response.Models))
	for _, model := range response.Models {
		if model.Visibility != "list" || strings.TrimSpace(model.Slug) == "" {
			continue
		}
		def := ModelDef{
			ID:            strings.TrimSpace(model.Slug),
			Name:          strings.TrimSpace(model.DisplayName),
			Provider:      OpenAI.Name,
			Mode:          ModeAgent,
			DefaultEffort: model.DefaultReasoningLevel,
			Priority:      model.Priority,
		}
		if def.Name == "" {
			def.Name = def.ID
		}
		if registry, ok := RegistryModelDef(OpenAI, ModeAgent, def.ID); ok {
			def.ReleaseDate = registry.ReleaseDate
			def.CapabilitiesKnown = registry.CapabilitiesKnown
			def.Reasoning = registry.Reasoning
			def.Temperature = registry.Temperature
			// models.dev is the source of truth for known model effort metadata.
			// Codex debug output remains useful for the locally visible models,
			// display names, and priority, but cannot override this catalog.
			def.SupportedEfforts = registry.SupportedEfforts
			def.DefaultEffort = registry.DefaultEffort
			if def.Priority == 0 {
				def.Priority = registry.Priority
			}
		} else {
			for _, level := range model.SupportedReasoningLevels {
				if level.Effort != api.EffortNone && level.Effort.Valid() {
					def.SupportedEfforts = append(def.SupportedEfforts, level.Effort)
				}
			}
			def.CapabilitiesKnown = true
			def.Reasoning = len(def.SupportedEfforts) > 0
		}
		models = append(models, def)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("codex debug models returned no visible models")
	}
	SortModelsByReleaseDateDesc(models)
	return models, nil
}
