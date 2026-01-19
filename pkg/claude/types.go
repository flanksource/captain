package claude

// HookEventType represents when a hook is triggered
type HookEventType string

const (
	HookEventPreToolUse       HookEventType = "PreToolUse"
	HookEventPostToolUse      HookEventType = "PostToolUse"
	HookEventNotification     HookEventType = "Notification"
	HookEventStop             HookEventType = "Stop"
	HookEventSubagentStop     HookEventType = "SubagentStop"
	HookEventUserPromptSubmit HookEventType = "UserPromptSubmit"
)

// PermissionMode represents Claude's current permission mode
type PermissionMode string

const (
	PermissionModeDefault     PermissionMode = "default"
	PermissionModePlan        PermissionMode = "plan"
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"
	PermissionModeBypassAll   PermissionMode = "bypassPermissions"
)

// PermissionDecision represents a hook's permission decision
type PermissionDecision string

const (
	PermissionAllow PermissionDecision = "allow"
	PermissionDeny  PermissionDecision = "deny"
	PermissionAsk   PermissionDecision = "ask"
)

// HookType represents the type of hook execution
type HookType string

const (
	HookTypeCommand HookType = "command"
	HookTypePrompt  HookType = "prompt"
)

// MessageRole represents who sent a message
type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// ContentType represents the type of content block
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
)

// StopReason represents why Claude stopped
type StopReason string

const (
	StopReasonEndTurn   StopReason = "end_turn"
	StopReasonToolUse   StopReason = "tool_use"
	StopReasonMaxTokens StopReason = "max_tokens"
	StopReasonError     StopReason = "error"
)
