package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// modelsJSON is the generated model catalog produced by `task models:update`
// (pkg/ai/internal/gen-model-registry). It is the models.dev snapshot with that
// generator's patches.json overlaid. Regenerate it after editing patches.json.
//
//go:embed models.json
var modelsJSON []byte

// KnownModel is one row of the generated catalog: a provider-native model id
// plus the capability metadata captain needs to route, price, and gate it.
//
// Aliases and SupersededBy are data, not code, on purpose. They used to be
// hardcoded switches in pkg/ai, which meant pkg/api's spec decoder could not see
// them and rejected models the pkg/ai selector accepted — `--model agent:sol`
// errored while the same value in prompt frontmatter ran. Keeping them here is
// what lets a single parser serve both entry points.
type KnownModel struct {
	ID              string   `json:"id"`
	Provider        string   `json:"provider"`
	Family          string   `json:"family"`
	Version         string   `json:"version"`
	Label           string   `json:"label"`
	ReleaseDate     string   `json:"releaseDate,omitempty"`
	Reasoning       bool     `json:"reasoning,omitempty"`
	Temperature     bool     `json:"temperature,omitempty"`
	ContextWindow   int      `json:"contextWindow,omitempty"`
	InputMediaTypes []string `json:"inputMediaTypes,omitempty"`
	// Cost is the model's published list price. Nil means the catalog snapshot
	// carries no price for this id — a miss, never a free model.
	Cost             *ModelCost `json:"cost,omitempty"`
	Preferred        bool       `json:"preferred,omitempty"`
	AdaptiveThinking bool       `json:"adaptiveThinking,omitempty"`
	Availability     []string   `json:"availability,omitempty"`
	SupportedEfforts []Effort   `json:"supportedEfforts,omitempty"`
	DefaultEffort    Effort     `json:"defaultEffort,omitempty"`
	Priority         int        `json:"priority,omitempty"`

	// Aliases are extra input tokens that resolve to this model — the codenames
	// "sol"/"terra"/"luna" on the gpt-5.6-* line. Input conveniences only: an
	// alias is never returned to a caller.
	Aliases []string `json:"aliases,omitempty"`
	// SupersededBy names the model that replaces this one. A superseded id still
	// parses, so old specs keep loading, but resolves to its successor.
	SupersededBy string `json:"supersededBy,omitempty"`
}

// knownModels is the parsed model catalog. It is a package-level var (not an
// init) so Go's initialization ordering guarantees it is populated before the
// provider descriptors, which index it.
var knownModels = mustParseModels(modelsJSON)

func mustParseModels(data []byte) []KnownModel {
	var models []KnownModel
	if err := json.Unmarshal(data, &models); err != nil {
		panic(fmt.Sprintf("pkg/api/registry: invalid embedded models.json: %v", err))
	}
	if len(models) == 0 {
		panic("pkg/api/registry: embedded models.json is empty")
	}
	return models
}

// KnownModels returns every catalog row. The slice is copied so callers cannot
// mutate the registry.
func KnownModels() []KnownModel {
	return append([]KnownModel(nil), knownModels...)
}
