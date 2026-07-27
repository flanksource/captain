package claude

import "github.com/segmentio/encoding/json"

// HooksConfig is the top-level hooks configuration
type HooksConfig struct {
	Hooks map[HookEventType][]HookMatcher `json:"hooks"`
}

// HookMatcher matches tools/events and specifies hooks to run
type HookMatcher struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []Hook `json:"hooks"`
}

// Hook defines a single hook to execute
type Hook struct {
	Type    HookType `json:"type"`
	Command string   `json:"command,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Timeout int      `json:"timeout,omitempty"`
}

// HookInput is the JSON passed to hooks via stdin
type HookInput struct {
	SessionID      string          `json:"session_id"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput     json.RawMessage `json:"tool_output,omitempty"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	StopHookReason string          `json:"stop_hook_reason,omitempty"`
	Prompt         string          `json:"prompt,omitempty"`
	CWD            string          `json:"cwd,omitempty"`
	HookEventName  string          `json:"hook_event_name,omitempty"`
	// Source is set on SessionStart: startup|resume|clear|compact.
	Source string `json:"source,omitempty"`
	// Reason is set on SessionEnd: clear|resume|logout|prompt_input_exit|other.
	Reason               string `json:"reason,omitempty"`
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
}

// HookOutput is the JSON returned by hooks
type HookOutput struct {
	Continue           bool                `json:"continue"`
	StopReason         string              `json:"stopReason,omitempty"`
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput contains tool-specific hook results
type HookSpecificOutput struct {
	PermissionDecision       PermissionDecision `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string             `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             json.RawMessage    `json:"updatedInput,omitempty"`
}

// BashToolInput represents the input for Bash tool
type BashToolInput struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

// EditToolInput represents the input for Edit tool
type EditToolInput struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// WriteToolInput represents the input for Write tool
type WriteToolInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// ReadToolInput represents the input for Read tool
type ReadToolInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}
