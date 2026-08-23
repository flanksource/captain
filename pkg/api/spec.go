package api

import (
	"encoding/json"
	"fmt"
	"reflect"
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
	// ToolPolicy is the ordered, last-match-wins rule list governing tool
	// authority. It supersedes ToolPreferences' flat exact-name map, but both are
	// accepted: ResolveDefinitions lowers the map through FromPreferences and
	// evaluates one list, so the two shapes cannot disagree about a tool.
	ToolPolicy   PermissionPolicy    `json:"toolPolicy,omitempty" yaml:"toolPolicy,omitempty" pretty:"-"`
	ToolApproval *ToolApprovalResume `json:"toolApproval,omitempty" yaml:"toolApproval,omitempty" pretty:"-"`
	Setup           *shell.Setup        `json:"setup,omitempty" yaml:"setup,omitempty"`

	// Sandbox selects the sandbox backend the run executes under. Absent = the
	// configured default, ultimately "none".
	Sandbox *SandboxRef `json:"sandbox,omitempty" yaml:"sandbox,omitempty"`

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

type specMarshal struct {
	Model       `json:",inline" yaml:",inline"`
	Prompt      *Prompt             `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Messages    []Message           `json:"messages,omitempty" yaml:"messages,omitempty"`
	Budget      *Budget             `json:"budget,omitempty" yaml:"budget,omitempty"`
	Memory      *Memory             `json:"memory,omitempty" yaml:"memory,omitempty"`
	Permissions *Permissions        `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	Preferences *ToolPreferences    `json:"toolPreferences,omitempty" yaml:"toolPreferences,omitempty"`
	ToolPolicy  PermissionPolicy    `json:"toolPolicy,omitempty" yaml:"toolPolicy,omitempty"`
	Approval    *ToolApprovalResume `json:"toolApproval,omitempty" yaml:"toolApproval,omitempty"`
	Setup       *shell.Setup        `json:"setup,omitempty" yaml:"setup,omitempty"`
	Sandbox     *SandboxRef         `json:"sandbox,omitempty" yaml:"sandbox,omitempty"`
	Workflow    *Workflow           `json:"workflow,omitempty" yaml:"workflow,omitempty"`
	SessionID   string              `json:"sessionId,omitempty" yaml:"sessionId,omitempty"`
	CLIArgs     map[string]any      `json:"cliArgs,omitempty" yaml:"cliArgs,omitempty"`
}

// IsEmpty reports whether v carries no instruction: every exported, serialized
// field is itself empty, recursively. It is the emptiness test the spec
// marshallers use, exported so callers layering configuration ask the struct
// definition rather than maintaining their own list of fields to check — a list
// that goes stale the moment a field is added.
//
// Two domain rules it already knows: a Tools block is empty iff it yields no
// policies, and an MCP block is empty iff it is not disabled and names no
// servers or modes. Fields tagged json:"-" or yaml:"-" are runtime state rather
// than configuration and do not count towards non-emptiness.
func IsEmpty(v any) bool {
	return isEmpty(reflect.ValueOf(v))
}

func isEmpty(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return true
		}
		value = value.Elem()
	}
	if value.CanInterface() {
		switch typed := value.Interface().(type) {
		case Tools:
			return len(typed.Policies()) == 0
		case MCP:
			return !typed.Disabled && len(typed.Servers) == 0 && len(typed.Modes) == 0
		}
	}
	switch value.Kind() {
	case reflect.Array:
		for i := range value.Len() {
			if !isEmpty(value.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Struct:
		exported := 0
		for i := range value.NumField() {
			field := value.Type().Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" || field.Tag.Get("yaml") == "-" {
				continue
			}
			exported++
			if !isEmpty(value.Field(i)) {
				return false
			}
		}
		return exported > 0 || value.IsZero()
	default:
		return value.IsZero()
	}
}

func omitEmptyValue[T any](value T) *T {
	if isEmpty(reflect.ValueOf(value)) {
		return nil
	}
	return &value
}

func omitEmptyPointer[T any](value *T) *T {
	if isEmpty(reflect.ValueOf(value)) {
		return nil
	}
	return value
}

func (s Spec) marshalValue() specMarshal {
	return specMarshal{
		Model:       s.Model,
		Prompt:      omitEmptyValue(s.Prompt),
		Messages:    s.Messages,
		Budget:      omitEmptyValue(s.Budget),
		Memory:      omitEmptyValue(s.Memory),
		Permissions: omitEmptyValue(s.Permissions),
		Preferences: omitEmptyValue(s.ToolPreferences),
		ToolPolicy:  s.ToolPolicy,
		Approval:    omitEmptyPointer(s.ToolApproval),
		Setup:       omitEmptyPointer(s.Setup),
		Sandbox:     omitEmptyPointer(s.Sandbox),
		Workflow:    omitEmptyPointer(s.Workflow),
		SessionID:   s.SessionID,
		CLIArgs:     s.CLIArgs,
	}
}

func (s Spec) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.marshalValue())
}

func (s Spec) MarshalYAML() (any, error) {
	return s.marshalValue(), nil
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
	if err := s.ToolPolicy.Validate(); err != nil {
		return err
	}
	if err := s.Workflow.Validate(); err != nil {
		return fmt.Errorf("workflow: %w", err)
	}
	if s.Sandbox != nil {
		if err := s.Sandbox.Validate(); err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
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
