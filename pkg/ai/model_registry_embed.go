package ai

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// modelRegistryJSON is the generated model catalog produced by
// `task models:update` (pkg/ai/internal/gen-model-registry). It is the
// models.dev snapshot with pkg/ai/internal/gen-model-registry/patches.json
// overlaid. Regenerate it after editing patches.json.
//
//go:embed model_registry.json
var modelRegistryJSON []byte

// exactModelRegistry is the parsed model catalog. It is a package-level var (not
// an init) so Go's initialization ordering guarantees it is populated before
// defaultCatalog, which references it via registryCatalogModels().
var exactModelRegistry = mustParseModelRegistry(modelRegistryJSON)

func mustParseModelRegistry(data []byte) []registryModel {
	var models []registryModel
	if err := json.Unmarshal(data, &models); err != nil {
		panic(fmt.Sprintf("pkg/ai: invalid embedded model_registry.json: %v", err))
	}
	if len(models) == 0 {
		panic("pkg/ai: embedded model_registry.json is empty")
	}
	return models
}
