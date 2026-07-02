package api

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/shell"
)

// Spec is the complete, structured specification of one model/agent run — the
// canonical shape the CLI flags and saved config build, and that ai.Request +
// ai.Config project onto. Model/Budget derive from ai.Config; Prompt/Memory/
// Permissions/Setup derive from ai.Request.
//
// Runtime-only concerns (API key and the CanUseTool callback) are deliberately
// excluded; they live in provider runtime config, not in this serializable
// domain object.
type Spec struct {
	Model       `json:",inline" yaml:",inline"`
	Prompt      Prompt       `json:"prompt" yaml:"prompt"`
	Budget      Budget       `json:"budget,omitempty" yaml:"budget,omitempty"`
	Memory      Memory       `json:"memory,omitempty" yaml:"memory,omitempty"`
	Permissions Permissions  `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	Setup       *shell.Setup `json:"setup,omitempty" yaml:"setup,omitempty"`

	// SessionID resumes an existing session. (ai.Request.SessionID)
	SessionID string `json:"sessionId,omitempty" yaml:"sessionId,omitempty" pretty:"label=Session"`

	// CLIArgs carries the "extra cmux args" (ClaudeCmuxOptions / CodexCmuxOptions)
	// keyed by their json field names — interactive CLI flags with no dedicated
	// Spec field. Ignored by non-cmux providers.
	CLIArgs map[string]any `json:"cliArgs,omitempty" yaml:"cliArgs,omitempty"`
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
	return nil
}

func (s Spec) Cwd() string {
	if s.Setup == nil {
		return ""
	}
	return s.Setup.Cwd
}

func (s *Spec) SetCwd(cwd string) {
	if s.Setup == nil {
		s.Setup = &shell.Setup{}
	}
	s.Setup.Cwd = cwd
}

func (s Spec) EnvMap() map[string]string {
	if s.Setup == nil || len(s.Setup.Env) == 0 {
		return nil
	}
	env := map[string]string{}
	for _, item := range s.Setup.Env {
		key, value, ok := strings.Cut(item, "=")
		if ok && key != "" {
			env[key] = value
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
