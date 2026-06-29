package api

import "fmt"

// Spec is the complete, structured specification of one model/agent run — the
// canonical shape the CLI flags and saved config build, and that the legacy
// ai.Request + ai.Config project onto. Model/Budget derive from ai.Config;
// Prompt/Memory/Permissions/Context derive from ai.Request.
//
// Runtime-only concerns (API key, cache settings, the CanUseTool callback) are
// deliberately excluded — they live in a separate runtime config, not in this
// serializable domain object.
type Spec struct {
	Model       `json:",inline" yaml:",inline"`
	Prompt      Prompt      `json:"prompt" yaml:"prompt"`
	Budget      Budget      `json:"budget,omitempty" yaml:"budget,omitempty"`
	Memory      Memory      `json:"memory,omitempty" yaml:"memory,omitempty"`
	Permissions Permissions `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	Context     Context     `json:"context,omitempty" yaml:"context,omitempty"`

	// SessionID resumes an existing session. (ai.Request.SessionID)
	SessionID string `json:"sessionId,omitempty" yaml:"sessionId,omitempty" pretty:"label=Session"`
	// MaxTurns caps agent turns; 0 = backend default. (ai.Request.MaxTurns)
	MaxTurns int `json:"maxTurns,omitempty" yaml:"maxTurns,omitempty" pretty:"label=Max Turns"`
}

// Validate runs each component's validation, failing loud on the first error.
func (s Spec) Validate() error {
	if err := s.Model.Validate(); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if err := s.Prompt.Validate(); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	if err := s.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if err := s.Permissions.Validate(); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	if s.MaxTurns < 0 || s.MaxTurns > 100 {
		return fmt.Errorf("invalid maxTurns %d (valid: 0-100, 0=backend default)", s.MaxTurns)
	}
	return nil
}
