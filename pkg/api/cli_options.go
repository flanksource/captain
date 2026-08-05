package api

import "fmt"

// This file models the interactive CLI flag surface of the cmux backends
// (claude-cmux / codex-cmux) as typed option structs. It covers only the flags
// that have NO api.Spec home ("extra cmux args"); flags that map onto the Spec
// (model, effort, permission mode, allow/deny tools, memory toggles) stay on the
// Spec and are emitted by the cmux provider from there — one source per flag.
//
// The structs carry three tags: json (form/value key + CLIArgs key), flag (the
// CLI flag the cmux command builder emits), and clicky (form presentation, read
// by clicky's rpc.SchemaForStruct). Enum fields supply their allowed values via a
// SchemaDescriber JSONSchema() method (clicky has no enum tag token).

// ClaudeCmuxOptions are the interactive `claude` flags with no api.Spec field.
type ClaudeCmuxOptions struct {
	Tools           []string `json:"tools,omitempty"              flag:"tools"                clicky:"title=Built-in Tools,desc=Restrict the built-in tool set (e.g. Bash Edit Read),order=1"`
	AddDir          []string `json:"addDir,omitempty"            flag:"add-dir"              clicky:"title=Additional Dirs,desc=Extra directories tools may access,order=2"`
	MCPConfig       []string `json:"mcpConfig,omitempty"         flag:"mcp-config"           clicky:"title=MCP Config,desc=Load MCP servers from JSON files or strings,order=3"`
	StrictMCPConfig bool     `json:"strictMcpConfig,omitempty"   flag:"strict-mcp-config"    clicky:"title=Strict MCP Config,desc=Only use MCP servers from --mcp-config,order=4"`
	Settings        string   `json:"settings,omitempty"          flag:"settings"             clicky:"title=Settings File/JSON,desc=Path to a settings JSON file or a JSON string,order=5"`
	SystemPrompt    string   `json:"systemPrompt,omitempty"      flag:"system-prompt"        clicky:"title=System Prompt,desc=Replace the default system prompt,order=6"`
	AppendSystem    string   `json:"appendSystemPrompt,omitempty" flag:"append-system-prompt" clicky:"title=Append System Prompt,desc=Append to the default system prompt,order=7"`
	Betas           []string `json:"betas,omitempty"             flag:"betas"                clicky:"title=Beta Headers,desc=Beta headers to include in API requests,order=8"`
	ExcludeDynamic  bool     `json:"excludeDynamicSystemPromptSections,omitempty" flag:"exclude-dynamic-system-prompt-sections" clicky:"title=Exclude Dynamic Sections,desc=Move per-machine sections to the first user message for prompt-cache reuse,order=9"`
	Agent           string   `json:"agent,omitempty"             flag:"agent"                clicky:"title=Agent,desc=Agent for the current session,order=10"`
	SafeMode        bool     `json:"safeMode,omitempty"          flag:"safe-mode"            clicky:"title=Safe Mode,desc=Start with all customizations disabled,order=11"`
}

// CodexCmuxOptions are the interactive `codex` extra args plus the codex posture
// flags (sandbox / ask-for-approval), which have no direct api.Spec field. The
// provider seeds Sandbox/AskForApproval defaults from Permissions.Mode via
// CodexSafety; an explicit value here overrides that default.
type CodexCmuxOptions struct {
	Sandbox        CodexSandbox        `json:"sandbox,omitempty"        flag:"sandbox"          clicky:"title=Sandbox,order=1"`
	AskForApproval CodexApprovalPolicy `json:"askForApproval,omitempty" flag:"ask-for-approval" clicky:"title=Approval Policy,order=2"`
	Config         []string            `json:"config,omitempty"         flag:"config"           clicky:"title=Config Overrides,desc=Override config values as key=value,order=3"`
	Profile        string              `json:"profile,omitempty"        flag:"profile"          clicky:"title=Profile,desc=Config profile to layer on the base config,order=4"`
	AddDir         []string            `json:"addDir,omitempty"         flag:"add-dir"          clicky:"title=Additional Dirs,desc=Extra writable directories,order=5"`
	Enable         []string            `json:"enable,omitempty"         flag:"enable"           clicky:"title=Enable Features,desc=Enable named features,order=6"`
	Disable        []string            `json:"disable,omitempty"        flag:"disable"          clicky:"title=Disable Features,desc=Disable named features,order=7"`
	StrictConfig   bool                `json:"strictConfig,omitempty"   flag:"strict-config"    clicky:"title=Strict Config,desc=Error on unrecognized config fields,order=8"`
	Search         bool                `json:"search,omitempty"         flag:"search"           clicky:"title=Web Search,desc=Enable live web search,order=9"`
	OSS            bool                `json:"oss,omitempty"            flag:"oss"              clicky:"title=OSS Provider,desc=Use an open-source model provider,order=10"`
	Image          []string            `json:"image,omitempty"          flag:"image"            clicky:"title=Images,desc=Image files to attach to the initial prompt,order=11"`
}

// CodexSandbox is codex's --sandbox policy (from `codex --help`).
type CodexSandbox string

const (
	CodexSandboxReadOnly       CodexSandbox = "read-only"
	CodexSandboxWorkspaceWrite CodexSandbox = "workspace-write"
	CodexSandboxDangerFull     CodexSandbox = "danger-full-access"
)

// AllCodexSandboxes lists the sandbox policies in ascending permissiveness.
func AllCodexSandboxes() []CodexSandbox {
	return []CodexSandbox{CodexSandboxReadOnly, CodexSandboxWorkspaceWrite, CodexSandboxDangerFull}
}

// JSONSchema implements clicky's rpc.SchemaDescriber so the form renders an enum.
func (CodexSandbox) JSONSchema() map[string]any {
	return enumSchema(
		codexSandboxValues(),
		map[string]string{
			string(CodexSandboxReadOnly):       "Read-only",
			string(CodexSandboxWorkspaceWrite): "Workspace write",
			string(CodexSandboxDangerFull):     "Danger: full access",
		},
		string(CodexSandboxReadOnly),
		"Sandbox policy for model-run shell commands",
	)
}

// CodexApprovalPolicy is codex's --ask-for-approval policy (from `codex --help`).
type CodexApprovalPolicy string

const (
	CodexApprovalUntrusted CodexApprovalPolicy = "untrusted"
	CodexApprovalOnFailure CodexApprovalPolicy = "on-failure"
	CodexApprovalOnRequest CodexApprovalPolicy = "on-request"
	CodexApprovalNever     CodexApprovalPolicy = "never"
)

// AllCodexApprovalPolicies lists the approval policies in canonical order.
func AllCodexApprovalPolicies() []CodexApprovalPolicy {
	return []CodexApprovalPolicy{CodexApprovalUntrusted, CodexApprovalOnFailure, CodexApprovalOnRequest, CodexApprovalNever}
}

// JSONSchema implements clicky's rpc.SchemaDescriber so the form renders an enum.
func (CodexApprovalPolicy) JSONSchema() map[string]any {
	return enumSchema(
		codexApprovalValues(),
		map[string]string{
			string(CodexApprovalUntrusted): "Untrusted only",
			string(CodexApprovalOnFailure): "On failure (deprecated)",
			string(CodexApprovalOnRequest): "On request",
			string(CodexApprovalNever):     "Never",
		},
		string(CodexApprovalOnRequest),
		"When codex asks for human approval before running a command",
	)
}

// CodexSafety maps the permission posture onto codex's sandbox + approval policy.
// It is the single source of truth shared by the codex app-server and cmux
// providers, so the two backends stay consistent.
func CodexSafety(p Permissions) (CodexSandbox, CodexApprovalPolicy) {
	switch {
	case p.Mode == PermissionBypass:
		return CodexSandboxDangerFull, CodexApprovalNever
	case p.Mode == PermissionAcceptEdits, p.Mode == PermissionAuto:
		return CodexSandboxWorkspaceWrite, CodexApprovalOnRequest
	case p.HasPreset(PresetEdit) && p.Mode == "":
		return CodexSandboxWorkspaceWrite, CodexApprovalOnRequest
	default:
		return CodexSandboxReadOnly, CodexApprovalOnRequest
	}
}

// CLIOptionsFor returns the zero-value option struct for a cmux backend, for
// reflecting into a form schema. It fails loud on any non-cmux backend.
func CLIOptionsFor(b Backend) (any, error) {
	switch b {
	case BackendClaudeCmux:
		return ClaudeCmuxOptions{}, nil
	case BackendCodexCmux:
		return CodexCmuxOptions{}, nil
	default:
		return nil, fmt.Errorf("backend %q has no cmux CLI options (valid: %s, %s)", b, BackendClaudeCmux, BackendCodexCmux)
	}
}

func codexSandboxValues() []string {
	out := make([]string, 0, len(AllCodexSandboxes()))
	for _, v := range AllCodexSandboxes() {
		out = append(out, string(v))
	}
	return out
}

func codexApprovalValues() []string {
	out := make([]string, 0, len(AllCodexApprovalPolicies()))
	for _, v := range AllCodexApprovalPolicies() {
		out = append(out, string(v))
	}
	return out
}

// enumSchema builds the SchemaDescriber map for a string enum: the standard
// type/enum/default/description keywords plus clicky's x-enum-* display hints.
func enumSchema(values []string, labels map[string]string, def, desc string) map[string]any {
	enum := make([]any, len(values))
	for i, v := range values {
		enum[i] = v
	}
	m := map[string]any{
		"type":           "string",
		"enum":           enum,
		"x-enum-display": "radio",
		"default":        def,
	}
	if desc != "" {
		m["description"] = desc
	}
	if len(labels) > 0 {
		m["x-enum-labels"] = labels
	}
	return m
}
