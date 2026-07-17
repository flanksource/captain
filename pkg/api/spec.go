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
	Prompt      Prompt      `json:"prompt" yaml:"prompt"`
	Messages    []Message   `json:"messages,omitempty" yaml:"messages,omitempty" pretty:"-"`
	Budget      Budget      `json:"budget,omitempty" yaml:"budget,omitempty"`
	Memory      Memory      `json:"memory,omitempty" yaml:"memory,omitempty"`
	Permissions Permissions `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	// ToolPreferences is the serializable per-turn tool/group selection policy.
	// Executable tool handlers remain in Config.Tools.
	ToolPreferences ToolPreferences     `json:"toolPreferences,omitempty" yaml:"toolPreferences,omitempty" pretty:"-"`
	ToolApproval    *ToolApprovalResume `json:"toolApproval,omitempty" yaml:"toolApproval,omitempty" pretty:"-"`
	Setup           *shell.Setup        `json:"setup,omitempty" yaml:"setup,omitempty"`

	// Workflow declares the generate→verify loop (verification + finalize) around
	// the run. Absent = single generation, no verification.
	Workflow *Workflow `json:"workflow,omitempty" yaml:"workflow,omitempty"`

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
	if s.ToolApproval != nil {
		if s.hasPromptBody() || len(s.Messages) > 0 {
			return fmt.Errorf("tool approval resume state, prompt body, and messages are mutually exclusive request modes")
		}
		if err := s.ToolApproval.Validate(); err != nil {
			return fmt.Errorf("tool approval: %w", err)
		}
		if err := s.Prompt.SchemaStrictness.Validate(); err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
	} else if len(s.Messages) > 0 {
		if err := s.ValidateRequestMode(); err != nil {
			return err
		}
		if err := ValidateMessages(s.Messages); err != nil {
			return fmt.Errorf("messages: %w", err)
		}
		if err := s.Prompt.SchemaStrictness.Validate(); err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
		// A verify-only spec (no body, workflow.verify present) legitimately has an
		// empty prompt; only its strictness setting is checked.
	} else if s.IsVerifyOnly() {
		if err := s.Prompt.SchemaStrictness.Validate(); err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
	} else if err := s.Prompt.Validate(); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	if err := s.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if err := s.Permissions.Validate(); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	if err := s.ToolPreferences.Validate(); err != nil {
		return err
	}
	if err := s.Workflow.Validate(); err != nil {
		return fmt.Errorf("workflow: %w", err)
	}
	return nil
}

// IsVerifyOnly reports whether the spec has no prompt body but declares a
// verification — a verify-only run that skips generation and verifies the
// current state (e.g. scoring already-committed work).
func (s Spec) IsVerifyOnly() bool {
	return s.ToolApproval == nil && len(s.Messages) == 0 && s.Prompt.User == "" && len(s.Prompt.Attachments) == 0 && s.Workflow != nil && s.Workflow.Verify != nil
}

func (s Spec) hasPromptBody() bool {
	return s.Prompt.User != "" || s.Prompt.System != "" || s.Prompt.AppendSystem != "" || len(s.Prompt.Attachments) > 0
}

// ValidateRequestMode rejects mixing canonical conversation history with the
// single-turn prompt body.
func (s Spec) ValidateRequestMode() error {
	if s.ToolApproval != nil && (len(s.Messages) > 0 || s.hasPromptBody()) {
		return fmt.Errorf("tool approval resume state, prompt body, and messages are mutually exclusive request modes")
	}
	if len(s.Messages) > 0 && s.hasPromptBody() {
		return fmt.Errorf("prompt body and messages are mutually exclusive request modes")
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
