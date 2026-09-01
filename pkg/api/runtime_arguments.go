package api

import (
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
)

// RuntimeArgumentImplementation describes how Captain supplies one native
// runtime option. Mapped values come from a serializable Spec path; managed
// values are fixed transport details or runtime-owned derived state.
type RuntimeArgumentImplementation string

const (
	ArgumentMapped  RuntimeArgumentImplementation = "mapped"
	ArgumentManaged RuntimeArgumentImplementation = "managed"
)

// RuntimeArgumentMapping connects a native CLI flag or agent protocol option to
// the Captain setting that supplies it.
type RuntimeArgumentMapping struct {
	Name           string                        `json:"name"`
	Implementation RuntimeArgumentImplementation `json:"implementation"`
	Description    string                        `json:"description,omitempty"`
}

type runtimeArgumentBinding struct {
	RuntimeArgumentMapping
	source string
}

func mappedArgument(name, source string) runtimeArgumentBinding {
	return runtimeArgumentBinding{
		RuntimeArgumentMapping: RuntimeArgumentMapping{Name: name, Implementation: ArgumentMapped},
		source:                 source,
	}
}

func managedArgument(name, description string) runtimeArgumentBinding {
	return runtimeArgumentBinding{RuntimeArgumentMapping: RuntimeArgumentMapping{
		Name: name, Implementation: ArgumentManaged, Description: description,
	}}
}

var claudeCLIArguments = []runtimeArgumentBinding{
	managedArgument("--print", "Captain always runs Claude headlessly."),
	managedArgument("--verbose", "Required for stream-json events."),
	managedArgument("--output-format stream-json", "Captain consumes Claude's event stream."),
	mappedArgument("--model", "model"),
	mappedArgument("--system-prompt", "prompt.system"),
	mappedArgument("--append-system-prompt", "prompt.appendSystem"),
	mappedArgument("--resume", "sessionId"),
	mappedArgument("--effort", "effort"),
	mappedArgument("--max-budget-usd", "budget.cost"),
	mappedArgument("--permission-mode", "permissions.mode"),
	mappedArgument("--allowedTools", "permissions.tools"),
	mappedArgument("--disallowedTools", "permissions.tools"),
	mappedArgument("--plugin-dir", "memory.skills|permissions.skills"),
	mappedArgument("--disable-slash-commands", "memory.skipSkills"),
	mappedArgument("--bare", "memory.bare"),
	mappedArgument("--mcp-config", "permissions.mcp.disabled"),
	mappedArgument("--strict-mcp-config", "permissions.mcp.disabled"),
	managedArgument("--settings", "Captain injects lifecycle monitor hooks when enabled."),
	mappedArgument("--json-schema", "prompt.schema"),
	managedArgument("stdin", "Captain composes the user prompt and writes it to stdin."),
}

var claudeAgentArguments = []runtimeArgumentBinding{
	mappedArgument("cwd", "setup.cwd"),
	mappedArgument("model", "model"),
	mappedArgument("systemPrompt", "prompt.system"),
	mappedArgument("appendSystemPrompt", "prompt.appendSystem"),
	mappedArgument("allowedTools", "permissions.tools"),
	mappedArgument("disallowedTools", "permissions.tools"),
	mappedArgument("maxTurns", "budget.maxTurns"),
	mappedArgument("maxBudgetUsd", "budget.cost"),
	mappedArgument("permissionMode", "permissions.mode"),
	mappedArgument("sandbox", "sandbox.mode"),
	mappedArgument("resume", "sessionId"),
	managedArgument("approvalMode", "Captain selects ask only when a tool approval broker is attached."),
	mappedArgument("outputSchema", "prompt.schema"),
	managedArgument("monitorUrl", "Captain injects its lifecycle monitor endpoint when enabled."),
	managedArgument("mcpServers", "Captain projects registered caller tools into an MCP server."),
	managedArgument("prompt", "Captain sends the composed prompt and prepared attachments per turn."),
}

var codexCLIArguments = []runtimeArgumentBinding{
	managedArgument("exec --json", "Captain always runs Codex non-interactively with JSON events."),
	mappedArgument("--image", "prompt.attachments"),
	mappedArgument("model_reasoning_effort", "effort"),
	mappedArgument("--model", "model"),
	mappedArgument("--cd", "setup.cwd"),
	mappedArgument("--sandbox", "sandbox.mode"),
	mappedArgument("approval_policy", "permissions.mode"),
	mappedArgument("--ephemeral", "memory.skipMemory|memory.bare"),
	mappedArgument("--ignore-user-config", "memory.skipUser"),
	mappedArgument("--ignore-rules", "memory.skipProject|memory.skipHooks"),
	managedArgument("notify", "Captain injects its lifecycle notification command when enabled."),
	mappedArgument("--output-schema", "prompt.schema"),
	mappedArgument("resume", "sessionId"),
	managedArgument("stdin", "Captain composes the prompt and writes it to stdin."),
}

var codexAgentArguments = []runtimeArgumentBinding{
	mappedArgument("thread/start.cwd", "setup.cwd"),
	mappedArgument("thread/start.model", "model"),
	mappedArgument("thread/start.sandbox", "sandbox.mode"),
	mappedArgument("thread/start.approvalPolicy", "permissions.mode"),
	mappedArgument("thread/start.ephemeral", "memory.skipMemory|memory.bare"),
	mappedArgument("thread/resume.threadId", "sessionId"),
	mappedArgument("turn/start.model", "model"),
	mappedArgument("turn/start.effort", "effort"),
	mappedArgument("turn/start.outputSchema", "prompt.schema"),
	managedArgument("turn/start.input", "Captain composes text and prepared local-image inputs."),
	mappedArgument("config.mcp_servers", "permissions.mcp.disabled"),
	managedArgument("config.mcp_servers.caller", "Captain projects registered caller tools into an MCP server."),
}

var geminiCLIArguments = []runtimeArgumentBinding{
	managedArgument("--output-format stream-json", "Captain consumes Gemini's event stream."),
	mappedArgument("--model", "model"),
	mappedArgument("--approval-mode", "permissions.mode"),
	managedArgument("stdin", "Captain composes system and user text and writes it to stdin."),
	managedArgument("cwd", "The shared CLI launcher applies setup.cwd to the process."),
}

var claudeCmuxArguments = appendRuntimeArguments(claudeCLIArguments,
	mappedArgument("--tools", "cliArgs.tools"),
	mappedArgument("--add-dir", "cliArgs.addDir"),
	mappedArgument("--mcp-config", "cliArgs.mcpConfig"),
	mappedArgument("--strict-mcp-config", "cliArgs.strictMcpConfig"),
	mappedArgument("--settings", "cliArgs.settings"),
	mappedArgument("--system-prompt", "cliArgs.systemPrompt"),
	mappedArgument("--append-system-prompt", "cliArgs.appendSystemPrompt"),
	mappedArgument("--betas", "cliArgs.betas"),
	mappedArgument("--exclude-dynamic-system-prompt-sections", "cliArgs.excludeDynamicSystemPromptSections"),
	mappedArgument("--agent", "cliArgs.agent"),
	mappedArgument("--safe-mode", "cliArgs.safeMode"),
)

var codexCmuxArguments = appendRuntimeArguments(codexCLIArguments,
	mappedArgument("--sandbox", "cliArgs.sandbox"),
	mappedArgument("--ask-for-approval", "cliArgs.askForApproval"),
	mappedArgument("--config", "cliArgs.config"),
	mappedArgument("--profile", "cliArgs.profile"),
	mappedArgument("--add-dir", "cliArgs.addDir"),
	mappedArgument("--enable", "cliArgs.enable"),
	mappedArgument("--disable", "cliArgs.disable"),
	mappedArgument("--strict-config", "cliArgs.strictConfig"),
	mappedArgument("--search", "cliArgs.search"),
	mappedArgument("--oss", "cliArgs.oss"),
	mappedArgument("--image", "cliArgs.image"),
)

func appendRuntimeArguments(base []runtimeArgumentBinding, extra ...runtimeArgumentBinding) []runtimeArgumentBinding {
	out := make([]runtimeArgumentBinding, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

// RuntimeSchemaFor returns the JSON Schema surface supported by one runtime.
// Native bindings live on their owning fields as x-clicky-arguments, while
// transport-owned details live at the root as x-clicky-managed-arguments.
func RuntimeSchemaFor(p *ModelProvider, mode RuntimeMode) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	for _, path := range runtimeCommonPaths(p, mode) {
		insertRuntimeSchemaField(schema, path, RuntimeArgumentMapping{}, p, mode)
	}
	var managed []any
	for _, binding := range runtimeArgumentBindings(p, mode) {
		if binding.Implementation == ArgumentManaged {
			managed = append(managed, runtimeArgumentValue(binding.RuntimeArgumentMapping))
			continue
		}
		for _, source := range strings.Split(binding.source, "|") {
			insertRuntimeSchemaField(schema, strings.TrimSpace(source), binding.RuntimeArgumentMapping, p, mode)
		}
	}
	if len(managed) > 0 {
		schema["x-clicky-managed-arguments"] = managed
	}
	return schema
}

// runtimeArgumentBindings is the provider×mode argv matrix. Only the local
// transports bind arguments; the API mode has none.
var runtimeArgumentMatrix = map[Runtime][]runtimeArgumentBinding{
	RuntimeOf(Anthropic, ModeCLI):   claudeCLIArguments,
	RuntimeOf(Anthropic, ModeAgent): claudeAgentArguments,
	RuntimeOf(Anthropic, ModeCmux):  claudeCmuxArguments,
	RuntimeOf(OpenAI, ModeCLI):      codexCLIArguments,
	RuntimeOf(OpenAI, ModeAgent):    codexAgentArguments,
	RuntimeOf(OpenAI, ModeCmux):     codexCmuxArguments,
	RuntimeOf(Google, ModeCLI):      geminiCLIArguments,
}

func runtimeArgumentBindings(p *ModelProvider, mode RuntimeMode) []runtimeArgumentBinding {
	mappings, ok := runtimeArgumentMatrix[RuntimeOf(p, mode)]
	if !ok {
		return nil
	}
	mappings = append(mappings, runtimeSandboxArgumentBindings(p, mode)...)
	return append([]runtimeArgumentBinding(nil), mappings...)
}

func runtimeCommonPaths(p *ModelProvider, mode RuntimeMode) []string {
	paths := []string{
		"model", "mode", "temperature", "noCache", "fallbacks",
		"budget.maxTokens", "budget.timeout", "prompt.user", "setup.envVars",
	}
	if p != nil {
		if caps, found := p.Caps(mode); found {
			if caps.Resume {
				paths = append(paths, "sessionId")
			}
			if caps.CallerTools {
				paths = append(paths, "toolPreferences", "toolPolicy")
			}
		}
		if mode == registry.ModeAPI {
			paths = append(paths, "prompt.system", "prompt.appendSystem", "setup.cwd")
			if p != DeepSeek {
				paths = append(paths, "effort")
			}
		}
	}
	caps := PermissionCapabilitiesFor(RuntimeOf(p, mode))
	// The posture is a permissions field, not a sandbox one: it applies whether or
	// not the run is isolated.
	if len(SupportedPermissionModes(p, mode)) > 0 {
		paths = append(paths, "permissions.mode")
	}
	if caps.ToolPolicySupport(ProvenanceAgent, ToolPolicyAllow).Honoured() ||
		caps.ToolPolicySupport(ProvenanceAgent, ToolPolicyDeny).Honoured() {
		paths = append(paths, "permissions.tools")
	}
	if caps.ResourceSupport(ResourceKindMCP, ResourceDisabled).Honoured() {
		paths = append(paths, "permissions.mcp.disabled")
	}
	if caps.ResourceSupport(ResourceKindSkills, ResourceEnabled).Honoured() {
		paths = append(paths, "permissions.skills")
	}
	return append(paths, runtimeSandboxPaths(p, mode)...)
}

func insertRuntimeSchemaField(schema map[string]any, path string, argument RuntimeArgumentMapping, p *ModelProvider, mode RuntimeMode) {
	if path == "" {
		return
	}
	current := schema
	parts := strings.Split(path, ".")
	for i, part := range parts {
		properties := current["properties"].(map[string]any)
		next, exists := properties[part].(map[string]any)
		if !exists {
			next = map[string]any{}
			properties[part] = next
		}
		if i < len(parts)-1 {
			next["type"] = "object"
			pathPrefix := strings.Join(parts[:i+1], ".")
			if strings.HasPrefix(pathPrefix, "sandbox.") {
				next["title"] = runtimeFieldTitle(pathPrefix)
			}
			if _, exists := next["properties"]; !exists {
				next["properties"] = map[string]any{}
			}
			current = next
			continue
		}
		applyRuntimeFieldPresentation(next, path, p, mode)
		if argument.Name != "" {
			arguments, _ := next["x-clicky-arguments"].([]any)
			next["x-clicky-arguments"] = append(arguments, runtimeArgumentValue(argument))
		}
	}
}

func runtimeArgumentValue(argument RuntimeArgumentMapping) map[string]any {
	value := map[string]any{
		"name": argument.Name, "implementation": string(argument.Implementation),
	}
	if argument.Description != "" {
		value["description"] = argument.Description
	}
	return value
}

func applyRuntimeFieldPresentation(field map[string]any, path string, p *ModelProvider, mode RuntimeMode) {
	field["type"] = runtimeFieldType(path)
	field["title"] = runtimeFieldTitle(path)
	if display := runtimeFieldArrayDisplay(path); display != "" {
		field["x-array-display"] = display
	}
	if section := runtimeFieldSection(path); section != "" {
		field["x-clicky-section"] = section
	}
	if icon := runtimeFieldIcon(path); icon != "" {
		field["x-icon"] = icon
	}
	if strings.HasPrefix(path, "sandbox.") {
		applyRuntimeSandboxPresentation(field, path, p, mode)
	}
	if path == "effort" {
		field["enum"] = enumValues(AllEfforts())
		field["x-enum-display"] = "combobox"
	}
	if path == "permissions.mode" {
		field["enum"] = enumValues(SupportedPermissionModes(p, mode))
		field["x-enum-display"] = "segmented"
	}
	if field["type"] == "array" {
		switch path {
		case "fallbacks":
			field["items"] = map[string]any{"oneOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "object", "additionalProperties": true},
			}}
		case "prompt.attachments", "setup.envVars", "toolPolicy":
			field["items"] = map[string]any{"type": "object", "additionalProperties": true}
		default:
			field["items"] = map[string]any{"type": "string"}
		}
	}
	if field["type"] == "object" {
		switch path {
		case "permissions.tools", "toolPreferences":
			field["additionalProperties"] = map[string]any{
				"type": "string", "enum": enumValues(AllToolPolicies()),
			}
		case "permissions.skills":
			field["additionalProperties"] = map[string]any{
				"type": "string", "enum": enumValues(AllResourceModes()),
			}
		default:
			field["additionalProperties"] = true
		}
	}
}

func runtimeFieldType(path string) string {
	switch path {
	case "temperature", "budget.cost":
		return "number"
	case "budget.maxTokens", "budget.maxTurns":
		return "integer"
	case "noCache", "memory.skipMemory", "memory.bare", "memory.skipUser", "memory.skipProject",
		"memory.skipSkills",
		"memory.skipHooks", "permissions.mcp.disabled", "sandbox.policy.required",
		"sandbox.policy.filesystem.includeSystemTemp", "sandbox.policy.network.allowAllUnixSockets",
		"sandbox.policy.network.allowLocalBinding", "sandbox.policy.commands.allowUnsandboxed",
		"sandbox.policy.platform.allowAppleEvents", "sandbox.policy.platform.weakerNestedIsolation",
		"sandbox.policy.platform.weakerNetworkIsolation":
		return "boolean"
	case "fallbacks", "prompt.attachments", "memory.skills", "setup.envVars", "toolPolicy", "cliArgs.addDir",
		"cliArgs.betas", "cliArgs.excludeDynamicSystemPromptSections", "cliArgs.enable", "cliArgs.disable",
		"cliArgs.image", "sandbox.policy.filesystem.writableRoots", "sandbox.policy.filesystem.readableRoots",
		"sandbox.policy.filesystem.deniedReadRoots", "sandbox.policy.filesystem.deniedWriteRoots",
		"sandbox.policy.network.allowedDomains", "sandbox.policy.network.deniedDomains",
		"sandbox.policy.network.allowedUnixSockets", "sandbox.policy.network.allowedMachServices",
		"sandbox.policy.commands.excludedFromSandbox", "sandbox.policy.credentials.deniedFiles",
		"sandbox.dispatch.paths":
		return "array"
	case "sandbox.policy.network.httpProxyPort", "sandbox.policy.network.socksProxyPort", "sandbox.dispatch.maxAttempts":
		return "integer"
	case "prompt.schema", "permissions.tools", "permissions.skills", "toolPreferences",
		"cliArgs.config":
		return "object"
	default:
		return "string"
	}
}

func runtimeFieldSection(path string) string {
	switch {
	case path == "model", path == "mode", path == "temperature", path == "effort",
		path == "noCache", path == "fallbacks", path == "sessionId",
		strings.HasPrefix(path, "budget."), strings.HasPrefix(path, "memory."),
		path == "permissions.skills":
		return "model"
	case strings.HasPrefix(path, "prompt."):
		return "prompt"
	case path == "setup.cwd":
		return "workspace"
	case path == "setup.envVars":
		return "environment"
	case strings.HasPrefix(path, "sandbox."):
		return "sandbox"
	case strings.HasPrefix(path, "permissions."), path == "toolPreferences", path == "toolPolicy":
		return "permissions"
	case strings.HasPrefix(path, "cliArgs."):
		return "cli"
	default:
		return ""
	}
}
