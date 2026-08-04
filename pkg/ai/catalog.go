package ai

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/api"
)

// Model describes one entry in the chat model menu. captain owns the catalog
// (it is keyed on Backend, which is captain data); clicky/aichat consumes it via
// type aliases. API backends carry a provider-prefixed ID for storage/display
// stability, while CLI/agent backends carry the exact provider model ID that the
// local backend receives (never a family alias or synthetic backend prefix).
type Model struct {
	ID          string
	Backend     Backend
	Label       string // human-friendly menu label
	Reasoning   bool   // model honours Effort
	Temperature bool   // model honours the temperature sampling control
	// AdaptiveThinking marks Anthropic models that use the adaptive thinking
	// schema (thinking:{type:adaptive} + output_config.effort) instead of the
	// legacy enabled schema.
	AdaptiveThinking bool
	ContextWindow    int    // max context tokens, for a usage gauge's denominator
	ReleaseDate      string // YYYY-MM-DD release date, when known
	InputMediaTypes  []string
	SupportedEfforts []api.Effort
	DefaultEffort    api.Effort
	Priority         int
	// AgentModel is retained for backward compatibility with older registered
	// models. New catalog entries should use exact runtime IDs directly in ID and
	// leave AgentModel empty.
	AgentModel string
	// Default marks the catalog default. Exactly one entry sets it; its ID equals
	// DefaultModelID.
	Default bool
}

// IsAgent reports whether the model runs through captain's agent framework (a
// supervised local subprocess) rather than the in-process genkit API path. All
// non-API backends are agent/CLI backends.
func (m Model) IsAgent() bool { return m.Backend.Kind() == "cli" }

// BareID strips the "provider/" prefix from an API model id (so it joins to live
// API listings and pricing keys); agent ids have no prefix and are returned
// verbatim.
func (m Model) BareID() string {
	if m.IsAgent() {
		return m.ID
	}
	if i := strings.IndexByte(m.ID, '/'); i >= 0 {
		return m.ID[i+1:]
	}
	return m.ID
}

// DefaultModelID is the chat backend's default (captain's NewAnthropic default).
// The catalog entry with this ID sets Default: true. It is declared once, in the
// registry, so a picker seeding itself and a run resolving an unnamed model
// cannot answer differently.
const DefaultModelID = api.DefaultModelID

// defaultCatalog is projected from the internal exact model registry. The API
// rows keep provider prefixes for stable storage; CLI/agent rows keep exact
// backend model IDs without synthetic claude-agent/codex prefixes.
var defaultCatalog = registryCatalogModels()

var (
	modelRegistryMu sync.RWMutex
	catalog         = append([]Model(nil), defaultCatalog...)
)

// Catalog returns the registered model menu with the user's opt-out set applied.
//
// The filter runs here, at read time, because the stored slice is projected from
// the registry at package init — long before captainconfig.Load installs the
// disabled set — so anything baked in at build time would never see it.
func Catalog() []Model {
	disabled := Disabled()
	models := catalogSnapshot()
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if disabled.Model(m.Backend, bareProviderModelID(m.ID)) {
			continue
		}
		m.SupportedEfforts, m.DefaultEffort = enabledEfforts(m.SupportedEfforts, m.DefaultEffort)
		out = append(out, m)
	}
	return out
}

// catalogSnapshot returns every registered model before user opt-outs are
// applied. Execution paths use Catalog; descriptive menus use this snapshot so
// they can retain disabled choices and explain how to re-enable them.
func catalogSnapshot() []Model {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	return append([]Model(nil), catalog...)
}

// RegisterModel adds a model to the global catalog, or replaces the existing
// entry with the same ID while preserving its position in the menu.
func RegisterModel(model Model) error {
	return RegisterModels(model)
}

// RegisterModels adds models to the global catalog. Existing IDs are updated in
// place; new IDs are appended to the menu.
func RegisterModels(models ...Model) error {
	normalized, err := normalizeModels(models, false)
	if err != nil {
		return err
	}

	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()

	for _, model := range normalized {
		updated := false
		for i := range catalog {
			if catalog[i].ID == model.ID {
				catalog[i] = model
				updated = true
				break
			}
		}
		if !updated {
			catalog = append(catalog, model)
		}
	}
	return nil
}

// SetModelCatalog replaces the global catalog. Use RegisterModel(s) to extend
// the built-in catalog instead.
func SetModelCatalog(models []Model) error {
	normalized, err := normalizeModels(models, true)
	if err != nil {
		return err
	}

	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	catalog = append([]Model(nil), normalized...)
	return nil
}

// ResetModelCatalog restores the built-in catalog. Primarily for tests that
// temporarily install custom models.
func ResetModelCatalog() {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	catalog = append([]Model(nil), defaultCatalog...)
}

func normalizeModels(models []Model, rejectDuplicateIDs bool) ([]Model, error) {
	out := make([]Model, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		normalized, err := normalizeModel(model)
		if err != nil {
			return nil, err
		}
		if rejectDuplicateIDs && seen[normalized.ID] {
			return nil, fmt.Errorf("duplicate model ID %q", normalized.ID)
		}
		seen[normalized.ID] = true
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeModel(model Model) (Model, error) {
	model.ID = strings.TrimSpace(model.ID)
	model.Label = strings.TrimSpace(model.Label)
	model.ReleaseDate = strings.TrimSpace(model.ReleaseDate)
	if model.ID == "" {
		return Model{}, fmt.Errorf("model ID is required")
	}
	if model.Backend == "" {
		return Model{}, fmt.Errorf("model %q must set Backend (one of: %s)", model.ID, BackendList())
	}
	if !model.Backend.Valid() {
		return Model{}, fmt.Errorf("model %q has invalid backend %q; want one of: %s", model.ID, model.Backend, BackendList())
	}
	if model.Label == "" {
		model.Label = model.ID
	}
	model.InputMediaTypes = clampInputMediaTypes(model.Backend, model.InputMediaTypes)
	if model.ReleaseDate != "" {
		normalized := normalizeReleaseDate(model.ReleaseDate)
		if normalized == "" {
			return Model{}, fmt.Errorf("model %q has invalid release date %q; want YYYY-MM-DD", model.ID, model.ReleaseDate)
		}
		model.ReleaseDate = normalized
	}
	return model, nil
}

// LookupModel resolves a model id against the catalog. An empty id resolves to
// the default. Returns an error listing the menu on miss — fail loud, never
// silently substitute.
func LookupModel(id string) (Model, error) {
	if id == "" {
		id = DefaultModelID
	}
	models := Catalog()
	for _, m := range models {
		if m.ID == id {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("unknown model %q; available: %s", id, strings.Join(modelIDsFrom(models), ", "))
}

func modelIDsFrom(models []Model) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	sort.Strings(ids)
	return ids
}
