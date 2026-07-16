package api

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/collections"
)

// CodexAutoReviewModel is the internal model used by Codex approval reviewers.
// Its transcripts are implementation noise rather than user sessions.
const CodexAutoReviewModel = "codex-auto-review"

// Model identifies which LLM serves a request plus the per-request inference
// knobs. Maps onto the legacy ai.Config.Model + ai.Request.{Temperature,
// ReasoningEffort}.
type Model struct {
	// Name is the catalog model slug, e.g. "claude-sonnet-4-6"; it drives backend
	// inference and pricing lookup.
	Name string `json:"model" yaml:"model" jsonschema:"required" pretty:"label=Model"`

	// ID is the fully-qualified provider id when it differs from Name, e.g. the
	// genkit "anthropic/claude-sonnet-4-6" or a codex slug. Empty means use Name.
	ID string `json:"id,omitempty" yaml:"id,omitempty" pretty:"label=ID"`

	// Backend overrides backend inference from Name; empty means InferBackend(Name).
	Backend Backend `json:"backend,omitempty" yaml:"backend,omitempty" pretty:"label=Backend"`

	// Temperature is the sampling temperature in [0,2]. A pointer so an explicit
	// 0.0 is distinguishable from "unset" (fail loud, not a silent default).
	Temperature *float64 `json:"temperature,omitempty" yaml:"temperature,omitempty" pretty:"label=Temp"`

	// Effort is the reasoning effort for thinking-capable models.
	Effort Effort `json:"effort,omitempty" yaml:"effort,omitempty" jsonschema:"enum=,enum=low,enum=medium,enum=high,enum=xhigh,enum=max,enum=ultra" pretty:"label=Effort"`

	// NoCache disables model response caching for this run.
	NoCache bool `json:"noCache,omitempty" yaml:"noCache,omitempty" pretty:"label=No Cache"`

	// Fallbacks are alternative models tried in order when the primary fails with a
	// transient/unavailable error or its provider cannot be constructed. Each is a
	// full Model (own backend/effort/temperature); a fallback's own nested Fallbacks
	// are ignored. Populate it directly, via a comma-separated Name (see ExpandCSV),
	// or via the --fallback flag / fallbacks: frontmatter. Each entry may be a
	// compact string ("agent:opus:high") or the object form (see ModelList).
	Fallbacks ModelList `json:"fallbacks,omitempty" yaml:"fallbacks,omitempty" pretty:"label=Fallbacks"`
}

// ResolveBackend returns Backend when set, otherwise infers it from Name.
func (m Model) ResolveBackend() (Backend, error) {
	if m.Backend != "" {
		if !m.Backend.Valid() {
			return "", fmt.Errorf("invalid backend %q (valid: %s)", m.Backend, BackendList())
		}
		return m.Backend, nil
	}
	return InferBackend(m.Name)
}

// Temp returns the temperature and whether it was explicitly set (non-nil), so
// providers can distinguish an intentional 0.0 from "unset, use the default".
func (m Model) Temp() (float64, bool) {
	if m.Temperature == nil {
		return 0, false
	}
	return *m.Temperature, true
}

// Validate checks the model name is present, the knobs are in range, and every
// fallback is itself a well-formed model.
func (m Model) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("model name is required")
	}
	if err := m.validateKnobs(); err != nil {
		return err
	}
	for i, fb := range m.Fallbacks {
		if fb.Name == "" {
			return fmt.Errorf("fallback[%d]: model name is required", i)
		}
		if err := fb.validateKnobs(); err != nil {
			return fmt.Errorf("fallback[%d] %q: %w", i, fb.Name, err)
		}
	}
	return nil
}

// validateKnobs range-checks the per-request inference knobs shared by a primary
// model and each fallback (backend/temperature/effort), independent of Name.
func (m Model) validateKnobs() error {
	if m.Backend != "" && !m.Backend.Valid() {
		return fmt.Errorf("invalid backend %q (valid: %s)", m.Backend, BackendList())
	}
	if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
		return fmt.Errorf("invalid temperature %v (valid: 0.0-2.0)", *m.Temperature)
	}
	return m.Effort.Validate()
}

// ExpandCSV moves any comma-separated tail of Name into name-only Fallbacks
// (prepended, so CSV order is preserved ahead of an explicit Fallbacks list),
// leaving Name as the single primary. It is idempotent: a single-model Name is
// returned with only whitespace trimmed, and a second call is a no-op.
func (m Model) ExpandCSV() Model {
	names := splitCSV(m.Name)
	if len(names) == 0 {
		return m
	}
	m.Name = names[0]
	if len(names) == 1 {
		return m
	}
	tail := make(ModelList, 0, len(names)-1)
	for _, n := range names[1:] {
		tail = append(tail, Model{Name: n})
	}
	m.Fallbacks = append(tail, m.Fallbacks...)
	return m
}

// Candidates returns the ordered models to try: the ExpandCSV primary first, then
// each fallback. Fallbacks inherit the primary's Temperature/NoCache when unset,
// and Effort only when they belong to the same provider family. They keep their
// own Name/Backend (an empty Backend is inferred at construction from the fallback's
// own Name), clear ID, and have nested Fallbacks dropped. A length of 1 means "no fallback".
func (m Model) Candidates() []Model {
	m = m.ExpandCSV()
	primary := m
	primary.Fallbacks = nil
	out := make([]Model, 0, collections.SafeAdd(1, len(m.Fallbacks)))
	out = append(out, primary)
	for _, fb := range m.Fallbacks {
		fb.Fallbacks = nil
		fb.ID = ""
		if fb.Temperature == nil {
			fb.Temperature = m.Temperature
		}
		if fb.Effort == "" && modelProvider(fb) == modelProvider(m) {
			fb.Effort = m.Effort
		}
		if !fb.NoCache {
			fb.NoCache = m.NoCache
		}
		out = append(out, fb)
	}
	return out
}

func modelProvider(model Model) Backend {
	if provider := model.Backend.Provider(); provider != "" {
		return provider
	}
	backend, err := InferBackend(model.Name)
	if err != nil {
		return ""
	}
	return backend.Provider()
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
