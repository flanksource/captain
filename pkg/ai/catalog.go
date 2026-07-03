package ai

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Model describes one entry in the chat model menu. captain owns the catalog
// (it is keyed on Backend, which is captain data); clicky/aichat consumes it via
// type aliases. For API backends ID is the full "provider/model" id used by the
// genkit execution path (e.g. "anthropic/claude-sonnet-4-6"). For agent/CLI
// backends ID is the menu key (e.g. "claude-agent-sonnet") and AgentModel is the
// run-time slug when it differs from ID.
type Model struct {
	ID            string
	Backend       Backend
	Label         string // human-friendly menu label
	Reasoning     bool   // model honours Effort
	ContextWindow int    // max context tokens, for a usage gauge's denominator
	ReleaseDate   string // YYYY-MM-DD release date, when known
	// AgentModel is the model slug passed to the captain backend when it differs
	// from ID (e.g. menu id "codex-gpt-5-codex" → backend model "gpt-5-codex").
	// Empty means use ID. Unused for API backends.
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
// The catalog entry with this ID sets Default: true.
const DefaultModelID = "anthropic/claude-sonnet-5"

// defaultCatalog is the model menu: only the latest generally-available model
// per tier for each provider — no preview or superseded entries. API entries
// carry the genkit "provider/model" id (kept byte-identical so the web menu and
// stored-thread model ids stay stable); agent entries carry the captain backend
// explicitly so codex slugs (which look like gpt-*) are not misrouted.
//
// Provider currency (reviewed 2026-07-02):
//   - Anthropic: Fable 5 (most capable), Opus 4.8, Sonnet 5, Haiku 4.5. Mythos 5
//     is Project Glasswing invite-only, so it is intentionally excluded.
//   - OpenAI: GPT-5.5 (flagship) and GPT-5.4 mini. GPT-5.6 is preview-only.
//   - Google: keep recent Pro and Flash families visible separately so a newer
//     Flash model does not hide recent Pro entries.
var defaultCatalog = []Model{
	{ID: "anthropic/claude-fable-5", Backend: BackendAnthropic, Label: "Claude Fable 5", Reasoning: true, ContextWindow: 1000000, ReleaseDate: "2026-06-15"},
	{ID: "anthropic/claude-opus-4-8", Backend: BackendAnthropic, Label: "Claude Opus 4.8", Reasoning: true, ContextWindow: 1000000, ReleaseDate: "2026-04-15"},
	{ID: "anthropic/claude-sonnet-5", Backend: BackendAnthropic, Label: "Claude Sonnet 5", Reasoning: true, ContextWindow: 1000000, ReleaseDate: "2026-05-20", Default: true},
	{ID: "anthropic/claude-haiku-4-5", Backend: BackendAnthropic, Label: "Claude Haiku 4.5", Reasoning: true, ContextWindow: 200000, ReleaseDate: "2025-10-15"},
	{ID: "openai/gpt-5.5", Backend: BackendOpenAI, Label: "GPT-5.5", Reasoning: true, ContextWindow: 1000000, ReleaseDate: "2026-06-01"},
	{ID: "openai/gpt-5.4-mini", Backend: BackendOpenAI, Label: "GPT-5.4 mini", Reasoning: true, ContextWindow: 400000, ReleaseDate: "2026-05-15"},
	{ID: "googleai/gemini-2.5-pro", Backend: BackendGemini, Label: "Gemini 2.5 Pro", Reasoning: true, ContextWindow: 1048576, ReleaseDate: "2025-06-17"},
	{ID: "googleai/gemini-3.0-pro", Backend: BackendGemini, Label: "Gemini 3.0 Pro", Reasoning: true, ContextWindow: 1048576},
	{ID: "googleai/gemini-3.5-flash", Backend: BackendGemini, Label: "Gemini 3.5 Flash", Reasoning: true, ContextWindow: 1048576, ReleaseDate: "2026-06-10"},
	// DeepSeek is OpenAI-compatible; deepseek-chat is the non-thinking model and
	// deepseek-reasoner is the thinking model (reasoning selected by model, not
	// effort). IDs are stable aliases that always resolve to DeepSeek's latest.
	{ID: "deepseek/deepseek-chat", Backend: BackendDeepSeek, Label: "DeepSeek Chat", ContextWindow: 131072},
	{ID: "deepseek/deepseek-reasoner", Backend: BackendDeepSeek, Label: "DeepSeek Reasoner", Reasoning: true, ContextWindow: 131072},

	// Agent-framework models (captain pkg/ai StreamingProvider). These run a
	// supervised local subprocess that owns its own tools. IDs are tier aliases
	// (not version-pinned), so they always resolve to the installed CLI's latest.
	{ID: "claude-agent-sonnet", Backend: BackendClaudeAgent, Label: "Claude Agent · Sonnet", Reasoning: true, ContextWindow: 1000000, ReleaseDate: "2026-05-20"},
	{ID: "claude-agent-opus", Backend: BackendClaudeAgent, Label: "Claude Agent · Opus", Reasoning: true, ContextWindow: 1000000, ReleaseDate: "2026-04-15"},
	{ID: "claude-agent-haiku", Backend: BackendClaudeAgent, Label: "Claude Agent · Haiku", Reasoning: true, ContextWindow: 200000, ReleaseDate: "2025-10-15"},
	{ID: "codex-gpt-5-codex", Backend: BackendCodexCLI, AgentModel: "gpt-5-codex", Label: "Codex · GPT-5", Reasoning: true, ContextWindow: 400000, ReleaseDate: "2025-08-07"},
}

var (
	modelRegistryMu sync.RWMutex
	catalog         = append([]Model(nil), defaultCatalog...)
)

// Catalog returns the registered model menu.
func Catalog() []Model {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()

	out := make([]Model, len(catalog))
	copy(out, catalog)
	return out
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
