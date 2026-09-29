package api

import (
	"context"
	"fmt"
	"strings"
)

// ApprovalFunc answers a question the agent runtime cannot answer on its own:
// whether a tool, command, file change, or network call may proceed, what the
// person answers to a clarifying question, or what they enter for an MCP
// elicitation. A streaming provider calls it on the active turn's context, and
// returning an error ends the request without a decision.
//
// Approving a request may exceed the sandbox the run started with. The request
// says so through Escalates, so a host that approves by rule should check it.
// A request the run's permission policy denies never reaches the callback.
type ApprovalFunc func(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)

// ApprovalKind discriminates the payload an ApprovalRequest carries.
type ApprovalKind string

const (
	ApprovalKindCommand     ApprovalKind = "command"
	ApprovalKindTool        ApprovalKind = "tool"
	ApprovalKindFilesystem  ApprovalKind = "filesystem"
	ApprovalKindNetwork     ApprovalKind = "network"
	ApprovalKindPermissions ApprovalKind = "permissions"
	ApprovalKindQuestion    ApprovalKind = "question"
	ApprovalKindElicitation ApprovalKind = "elicitation"
	// ApprovalKindPlan is the agent asking to leave plan mode and implement its plan.
	ApprovalKindPlan ApprovalKind = "plan"
)

// Valid reports whether k is one of the declared kinds.
func (k ApprovalKind) Valid() bool {
	switch k {
	case ApprovalKindCommand, ApprovalKindTool, ApprovalKindFilesystem, ApprovalKindNetwork,
		ApprovalKindPermissions, ApprovalKindQuestion, ApprovalKindElicitation, ApprovalKindPlan:
		return true
	}
	return false
}

// ApprovalScope is how far an approval reaches. Empty means one request.
type ApprovalScope string

const (
	ApprovalScopeRequest ApprovalScope = "request"
	ApprovalScopeTurn    ApprovalScope = "turn"
	ApprovalScopeSession ApprovalScope = "session"
)

// Valid reports whether s is one of the declared scopes.
func (s ApprovalScope) Valid() bool {
	return s == ApprovalScopeRequest || s == ApprovalScopeTurn || s == ApprovalScopeSession
}

// ApprovalRequest describes what the agent is asking for. Tool, Input,
// ToolUseID and SessionID keep the meaning they had on the pre-rename
// PermissionRequest; Kind selects which one of the typed payloads is set.
type ApprovalRequest struct {
	// Tool is the native tool name, or "Elicitation" for an MCP elicitation.
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input,omitempty"`
	// ToolUseID is the native request identity the broker keys idempotency on.
	ToolUseID          string `json:"toolUseId,omitempty"`
	ToolUseIDGenerated bool   `json:"-"`
	SessionID          string `json:"sessionId,omitempty"`

	Kind            ApprovalKind    `json:"kind"`
	TurnID          string          `json:"turnId,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	Escalates       bool            `json:"escalates,omitempty"`
	Interruptible   bool            `json:"interruptible,omitempty"`
	SupportedScopes []ApprovalScope `json:"supportedScopes,omitempty"`

	// Info is the subject the deny pre-check resolves; it is not persisted.
	Info *ToolInfo `json:"-"`

	// COMPAT(unified-approval): remove in Phase 6.
	// LegacyContract marks a request the pre-rename CanUseTool contract already
	// delivered, so a callback registered through the deprecated entry points
	// still receives it.
	LegacyContract bool `json:"-"`

	Command     *CommandApproval     `json:"command,omitempty"`
	Filesystem  *FilesystemApproval  `json:"filesystem,omitempty"`
	Network     *NetworkApproval     `json:"network,omitempty"`
	Permissions *NativeSandboxPolicy `json:"permissions,omitempty"`
	Questions   []TerminalQuestion   `json:"questions,omitempty"`
	Elicitation *ElicitationApproval `json:"elicitation,omitempty"`
	// Plan is the plan the agent asks to implement. Approving it leaves plan mode;
	// a deny keeps planning and feeds Message back as the feedback.
	Plan *TerminalPlan `json:"plan,omitempty"`
}

// CommandApproval is a shell command, or input to one already running.
type CommandApproval struct {
	Command     string      `json:"command,omitempty"`
	Cwd         string      `json:"cwd,omitempty"`
	Unsandboxed bool        `json:"unsandboxed,omitempty"`
	Stdin       *StdinWrite `json:"stdin,omitempty"`
	// ProposedPolicy is the provider's suggested persistent rule. It is shown to
	// the host and never offered as an action.
	ProposedPolicy []string `json:"proposedPolicy,omitempty"`
}

// StdinWrite is input sent to a terminal a previous command started.
type StdinWrite struct {
	Terminal string `json:"terminal"`
	Chars    string `json:"chars"`
}

// FilesystemOperation names what a file approval does.
type FilesystemOperation string

const (
	FilesystemRead   FilesystemOperation = "read"
	FilesystemWrite  FilesystemOperation = "write"
	FilesystemEdit   FilesystemOperation = "edit"
	FilesystemDelete FilesystemOperation = "delete"
	FilesystemPatch  FilesystemOperation = "patch"
)

// FilesystemApproval is a file operation the agent wants to perform.
type FilesystemApproval struct {
	Operation FilesystemOperation `json:"operation"`
	Paths     []string            `json:"paths,omitempty"`
	Changes   []FileChange        `json:"changes,omitempty"`
}

// FileChange is one file a patch touches.
type FileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// NetworkApproval is outbound network access, with the command that needs it
// when there is one.
type NetworkApproval struct {
	Host     string `json:"host,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	URL      string `json:"url,omitempty"`
	Command  string `json:"command,omitempty"`
}

// ElicitationMode is how an MCP server collects input.
type ElicitationMode string

const (
	ElicitationModeForm ElicitationMode = "form"
	ElicitationModeURL  ElicitationMode = "url"
)

// ElicitationApproval is an MCP server asking the person for input.
type ElicitationApproval struct {
	Server  string          `json:"server"`
	Mode    ElicitationMode `json:"mode"`
	Message string          `json:"message"`
	// Schema is the requested form schema: a flat object of primitive fields.
	Schema        map[string]any `json:"schema,omitempty"`
	URL           string         `json:"url,omitempty"`
	ElicitationID string         `json:"elicitationId,omitempty"`
}

// ApprovalDecision answers an ApprovalRequest. Allow, Message and UpdatedInput
// keep their pre-rename meaning. Question answers travel as
// UpdatedInput["answers"] and form content as UpdatedInput itself.
type ApprovalDecision struct {
	Allow        bool           `json:"allow"`
	Message      string         `json:"message,omitempty"`
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
	// Interrupt denies the request and ends the turn.
	Interrupt bool `json:"interrupt,omitempty"`
	// Scope widens an approval beyond this request where the provider offers it.
	Scope ApprovalScope `json:"scope,omitempty"`
	// Grants is the approved subset of a permissions request; nil together with
	// Allow grants everything requested.
	Grants *NativeSandboxPolicy `json:"grants,omitempty"`
}

// Validate reports a request whose kind and payload disagree.
func (r ApprovalRequest) Validate() error {
	if r.Tool == "" {
		return fmt.Errorf("approval request needs a tool")
	}
	if !r.Kind.Valid() {
		return fmt.Errorf("approval request for %q has invalid kind %q", r.Tool, r.Kind)
	}
	for _, scope := range r.SupportedScopes {
		if !scope.Valid() {
			return fmt.Errorf("approval request for %q offers invalid scope %q", r.Tool, scope)
		}
	}
	set := map[ApprovalKind]bool{
		ApprovalKindCommand:     r.Command != nil,
		ApprovalKindFilesystem:  r.Filesystem != nil,
		ApprovalKindNetwork:     r.Network != nil,
		ApprovalKindPermissions: r.Permissions != nil,
		ApprovalKindQuestion:    len(r.Questions) > 0,
		ApprovalKindElicitation: r.Elicitation != nil,
		ApprovalKindPlan:        r.Plan != nil,
	}
	for kind, present := range set {
		if present && kind != r.Kind {
			return fmt.Errorf("%s approval request for %q carries a %s payload", r.Kind, r.Tool, kind)
		}
	}
	if r.Kind != ApprovalKindTool && !set[r.Kind] {
		return fmt.Errorf("%s approval request for %q is missing its %s payload", r.Kind, r.Tool, r.Kind)
	}
	if r.Plan != nil && strings.TrimSpace(r.Plan.Content) == "" {
		return fmt.Errorf("plan approval request for %q has no plan content", r.Tool)
	}
	return nil
}

func (r ApprovalRequest) supportsScope(scope ApprovalScope) bool {
	for _, supported := range r.SupportedScopes {
		if supported == scope {
			return true
		}
	}
	return false
}
