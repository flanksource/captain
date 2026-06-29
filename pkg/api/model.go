package api

import "fmt"

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
	Effort Effort `json:"effort,omitempty" yaml:"effort,omitempty" jsonschema:"enum=,enum=low,enum=medium,enum=high,enum=xhigh" pretty:"label=Effort"`
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

// Validate checks the model name is present and the knobs are in range.
func (m Model) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("model name is required")
	}
	if m.Backend != "" && !m.Backend.Valid() {
		return fmt.Errorf("invalid backend %q (valid: %s)", m.Backend, BackendList())
	}
	if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
		return fmt.Errorf("invalid temperature %v (valid: 0.0-2.0)", *m.Temperature)
	}
	return m.Effort.Validate()
}
