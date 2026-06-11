package aichat

import (
	"fmt"
	"sort"
	"strings"
)

// Provider is a Genkit provider name (the prefix of a "provider/model" id).
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderGoogle    Provider = "googleai"
)

// Effort is the per-request reasoning effort, translated per provider through
// Genkit's model config (Anthropic thinking budget, OpenAI reasoning_effort,
// Gemini thinkingConfig). Non-reasoning models ignore it.
type Effort string

const (
	EffortNone   Effort = ""
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// Model describes one entry in the chat model menu. ID is the full Genkit
// "provider/model" id passed to ai.WithModelName.
type Model struct {
	ID        string
	Provider  Provider
	Reasoning bool // model honours Effort
}

// DefaultModelID is the chat backend's default, mirroring captain pkg/ai
// (Anthropic claude-sonnet-4 is captain's NewAnthropic default).
const DefaultModelID = "anthropic/claude-sonnet-4-5"

// catalog is the v1 model menu. Mirrors captain pkg/ai provider defaults so the
// chat agrees with the rest of the stack.
var catalog = []Model{
	{ID: "anthropic/claude-sonnet-4-5", Provider: ProviderAnthropic, Reasoning: true},
	{ID: "anthropic/claude-opus-4-1", Provider: ProviderAnthropic, Reasoning: true},
	{ID: "anthropic/claude-3-5-haiku-latest", Provider: ProviderAnthropic, Reasoning: false},
	{ID: "openai/gpt-4o", Provider: ProviderOpenAI, Reasoning: false},
	{ID: "openai/o3", Provider: ProviderOpenAI, Reasoning: true},
	{ID: "openai/o4-mini", Provider: ProviderOpenAI, Reasoning: true},
	{ID: "googleai/gemini-2.5-pro", Provider: ProviderGoogle, Reasoning: true},
	{ID: "googleai/gemini-2.5-flash", Provider: ProviderGoogle, Reasoning: true},
}

// Catalog returns the configured model menu.
func Catalog() []Model {
	out := make([]Model, len(catalog))
	copy(out, catalog)
	return out
}

// LookupModel resolves a "provider/model" id against the catalog. Returns an
// error listing the menu on miss — fail loud, never silently substitute.
func LookupModel(id string) (Model, error) {
	if id == "" {
		id = DefaultModelID
	}
	for _, m := range catalog {
		if m.ID == id {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("unknown model %q; available: %s", id, strings.Join(modelIDs(), ", "))
}

// ProviderOf extracts the provider from a "provider/model" id without requiring
// the model to be in the catalog.
func ProviderOf(id string) Provider {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return Provider(id[:i])
	}
	return ""
}

func modelIDs() []string {
	ids := make([]string, len(catalog))
	for i, m := range catalog {
		ids[i] = m.ID
	}
	sort.Strings(ids)
	return ids
}

// ValidateEffort rejects unknown effort values (fail loud).
func ValidateEffort(e Effort) error {
	switch e {
	case EffortNone, EffortLow, EffortMedium, EffortHigh:
		return nil
	default:
		return fmt.Errorf("invalid reasoning effort %q; want one of: low, medium, high", e)
	}
}
