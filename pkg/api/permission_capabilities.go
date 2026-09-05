package api

// This file is the declared answer to "what can this runtime actually do with a
// permissions block?".
//
// It exists because every other configurable axis — model, effort, cliArgs,
// sandbox — is gated by a capability that is declared, served, and enforced,
// while permissions were offered unconditionally and reconciled only when a
// provider built argv. The mapping functions (CodexSafety here,
// cliClaudePermissionMode in the anthropic cli provider, geminiApprovalMode in the
// gemini one) remain the implementation; this table is the static declaration
// they are proven against, so drift becomes a failing test and a printed row
// rather than a surprise at dispatch.
//
// It deliberately describes what captain does *today*, warts included: a cell
// that reads SupportUnsupported is a statement about the current code, not an
// aspiration. Changing behaviour means changing a cell, which shows up in
// review as a diff to the matrix.
//
// It lives in pkg/api rather than pkg/api/registry because the vocabulary it is
// keyed by — PermissionMode, ToolPolicy, ResourceMode — lives here, and nothing
// below pkg/api consults it. The "completeness" specs in
// permission_capabilities_ginkgo_test.go pin this table against
// registry.AllRuntimes so a new runtime cannot be added there without a decision
// being made here.

// SupportKind is how faithfully a runtime honours one permission setting.
type SupportKind string

const (
	// SupportNative: the runtime expresses the setting exactly.
	SupportNative SupportKind = "native"
	// SupportApproximated: the runtime expresses something close, described by
	// Effects. The run is honoured, but not literally as written.
	SupportApproximated SupportKind = "approximated"
	// SupportRequiresBroker: enforceable only when an approval broker is
	// attached (Config.CanUseTool, or a cmux terminal interceptor). Without one
	// the setting cannot be honoured and the run must be refused.
	SupportRequiresBroker SupportKind = "requires-broker"
	// SupportUnsupported: the runtime cannot express it at all.
	SupportUnsupported SupportKind = "unsupported"
)

// PermissionEffects is the structured target state a setting compiles to. It is
// structured rather than prose so a test can compare it against what the mapper
// actually returns, and a UI can key off it instead of parsing a sentence.
type PermissionEffects struct {
	// Flag is the literal argv the runtime emits, when it emits one. An empty
	// Flag on a native mode means the runtime omits the flag deliberately and
	// inherits its own default.
	Flag string `json:"flag,omitempty"`
	// Sandbox and Approval are codex's two-part posture (CodexSafety).
	Sandbox  string `json:"sandbox,omitempty"`
	Approval string `json:"approval,omitempty"`
	// Note explains an approximation or a caveat a caller must know about.
	Note string `json:"note,omitempty"`
}

// Support is one cell: how a runtime honours one setting, and what it becomes.
type Support struct {
	Kind    SupportKind       `json:"kind"`
	Effects PermissionEffects `json:"effects,omitzero"`
}

// Honoured reports whether the setting reaches the agent in some form. A
// requires-broker cell is not honoured on its own: it depends on runtime state
// this table cannot see.
func (s Support) Honoured() bool {
	return s.Kind == SupportNative || s.Kind == SupportApproximated
}

// ToolProvenance is where a tool came from, which is what determines whether a
// per-tool policy can be enforced at all.
//
// This is the dimension a per-runtime boolean misses. Captain owns the MCP
// server it builds for caller tools, so it enforces a deny by simply not
// registering the tool — on runtimes whose own CLI has no tool filter at all.
type ToolProvenance string

const (
	// ProvenanceAgent is the agent CLI's own built-ins: claude's Read/Edit/Bash,
	// codex's shell/apply_patch. Only the CLI's own flags can filter these.
	ProvenanceAgent ToolProvenance = "agent"
	// ProvenanceCaller is a tool captain serves over its own MCP server.
	// ResolveDefinitions drops denied tools before the server is built, so a
	// deny is enforced by omission wherever caller tools are supported.
	ProvenanceCaller ToolProvenance = "caller"
	// ProvenanceMCP is a third-party server from .mcp.json or config.toml.
	// Captain controls whether the server loads, not which of its tools do —
	// until a captain-owned gateway proxies them.
	ProvenanceMCP ToolProvenance = "mcp"
)

// AllToolProvenances lists provenances in canonical order.
func AllToolProvenances() []ToolProvenance {
	return []ToolProvenance{ProvenanceAgent, ProvenanceCaller, ProvenanceMCP}
}

// ResourceKind is a class of loadable resource governed by enabled/disabled.
// This is the availability axis, distinct from the authority axis a ToolPolicy
// expresses: enabling an MCP server *creates* tools, which then carry policies.
type ResourceKind string

const (
	ResourceKindMCP     ResourceKind = "mcp"
	ResourceKindSkills  ResourceKind = "skills"
	ResourceKindPlugins ResourceKind = "plugins"
)

// AllResourceKinds lists resource kinds in canonical order.
func AllResourceKinds() []ResourceKind {
	return []ResourceKind{ResourceKindMCP, ResourceKindSkills, ResourceKindPlugins}
}

// AllResourceModes lists the availability values in canonical order.
func AllResourceModes() []ResourceMode {
	return []ResourceMode{ResourceEnabled, ResourceDisabled}
}

// PermissionCapabilities is one runtime's declared permission surface.
type PermissionCapabilities struct {
	// Modes is the per-posture support map. Every recognised PermissionMode has
	// an entry, so an absent key is a bug rather than "unsupported".
	Modes map[PermissionMode]Support `json:"modes"`
	// ToolPolicies is keyed by provenance first, because the same policy value
	// is enforceable from one source and not another on the same runtime.
	ToolPolicies map[ToolProvenance]map[ToolPolicy]Support `json:"toolPolicies"`
	// Resources is the availability axis, keyed by kind and then by the value
	// requested. Both keys matter because the two directions are independent and
	// MCP is only switchable *off* (there is no per-server enable), while disabled
	// skills are enforced by omission before reaching any provider. One Support per kind would
	// report "supported" for a request that is silently ignored.
	Resources map[ResourceKind]map[ResourceMode]Support `json:"resources"`
	// Tools is the runtime's built-in tool vocabulary — the names a per-tool
	// policy can legitimately mention for ProvenanceAgent.
	Tools []string `json:"tools,omitempty"`
}

// ModeSupport returns the cell for a posture, defaulting to unsupported for an
// unrecognised mode rather than a zero Support with an empty Kind.
func (c PermissionCapabilities) ModeSupport(mode PermissionMode) Support {
	if s, ok := c.Modes[mode]; ok {
		return s
	}
	return Support{Kind: SupportUnsupported}
}

// ToolPolicySupport returns the cell for one (provenance, policy) pair.
func (c PermissionCapabilities) ToolPolicySupport(p ToolProvenance, policy ToolPolicy) Support {
	if byPolicy, ok := c.ToolPolicies[p]; ok {
		if s, ok := byPolicy[policy]; ok {
			return s
		}
	}
	return Support{Kind: SupportUnsupported}
}

// ResourceSupport returns the cell for one (kind, requested value) pair.
func (c PermissionCapabilities) ResourceSupport(kind ResourceKind, mode ResourceMode) Support {
	if byMode, ok := c.Resources[kind]; ok {
		if s, ok := byMode[mode]; ok {
			return s
		}
	}
	return Support{Kind: SupportUnsupported}
}

// PermissionCapabilitiesFor returns the declared surface for a runtime. An
// unknown runtime gets a fully-unsupported row rather than a zero value, so a
// caller that forgets to check still fails closed.
func PermissionCapabilitiesFor(runtime Runtime) PermissionCapabilities {
	if caps, ok := permissionCapabilities[runtime]; ok {
		return caps
	}
	return PermissionCapabilities{
		Modes:        unsupportedModes(),
		ToolPolicies: toolPolicies(noToolFilter(), noToolFilter(), noToolFilter()),
		Resources:    resources(false, false),
	}
}

// PermissionCapabilityRuntimes lists the runtimes the table declares, in
// canonical AllRuntimes order.
func PermissionCapabilityRuntimes() []Runtime {
	out := make([]Runtime, 0, len(permissionCapabilities))
	for _, r := range AllRuntimes() {
		if _, ok := permissionCapabilities[r]; ok {
			out = append(out, r)
		}
	}
	return out
}

// --- construction helpers -------------------------------------------------
//
// The table below is dense by design: it is meant to be read as a matrix, and
// helpers keep each row to one line per setting so a reviewer can diff a cell.

func native(flag string) Support {
	return Support{Kind: SupportNative, Effects: PermissionEffects{Flag: flag}}
}

func nativeNote(flag, note string) Support {
	return Support{Kind: SupportNative, Effects: PermissionEffects{Flag: flag, Note: note}}
}

func unsupported(note string) Support {
	return Support{Kind: SupportUnsupported, Effects: PermissionEffects{Note: note}}
}

func broker(note string) Support {
	return Support{Kind: SupportRequiresBroker, Effects: PermissionEffects{Note: note}}
}

func codexPosture(sandbox, approval, note string) Support {
	return Support{
		Kind:    SupportApproximated,
		Effects: PermissionEffects{Sandbox: sandbox, Approval: approval, Note: note},
	}
}

func approxFlag(flag, note string) Support {
	return Support{Kind: SupportApproximated, Effects: PermissionEffects{Flag: flag, Note: note}}
}

func unsupportedModes() map[PermissionMode]Support {
	out := make(map[PermissionMode]Support, len(AllPermissionModes()))
	for _, m := range AllPermissionModes() {
		out[m] = unsupported("this runtime has no provider-native sandbox approval posture")
	}
	return out
}

func toolPolicies(agent, caller, mcp map[ToolPolicy]Support) map[ToolProvenance]map[ToolPolicy]Support {
	return map[ToolProvenance]map[ToolPolicy]Support{
		ProvenanceAgent:  agent,
		ProvenanceCaller: caller,
		ProvenanceMCP:    mcp,
	}
}

// noToolFilter is the row for a source captain cannot filter: auto constrains
// nothing so it is always fine, everything else is unenforceable.
func noToolFilter() map[ToolPolicy]Support {
	return map[ToolPolicy]Support{
		ToolPolicyAuto:  native(""),
		ToolPolicyAllow: unsupported("no tool filter for this source"),
		ToolPolicyDeny:  unsupported("no tool filter for this source"),
		ToolPolicyAsk:   unsupported("no per-tool prompt for this source"),
	}
}

// claudeAgentTools is the row for claude's own built-ins, the only agent
// built-ins any transport can filter.
func claudeAgentTools() map[ToolPolicy]Support {
	return map[ToolPolicy]Support{
		ToolPolicyAuto: native(""),
		ToolPolicyAllow: nativeNote("--allowedTools",
			"claude's --allowedTools auto-approves rather than restricting: unlisted tools are not denied by it"),
		ToolPolicyDeny: native("--disallowedTools"),
		ToolPolicyAsk:  unsupported("no per-tool prompt for agent built-ins on any transport"),
	}
}

// callerTools is the row for tools captain serves itself. Deny is enforced by
// omission in ResolveDefinitions, which needs no cooperation from the agent.
func callerTools() map[ToolPolicy]Support {
	return map[ToolPolicy]Support{
		ToolPolicyAuto:  native(""),
		ToolPolicyAllow: native("registered without an approval gate"),
		ToolPolicyDeny:  native("omitted from the served tool list"),
		ToolPolicyAsk:   broker("enforced through Config.CanUseTool when a broker is attached"),
	}
}

// resources builds the availability rows. Only two cells across the whole matrix
// are ever honoured, so they are the only two parameters:
//
//   - mcpOff: does `permissions.mcp.disabled` actually silence ambient servers.
//   - skillsOn: does `permissions.skills: {dir: enabled}` actually load them.
//
// Disabled skills are enforced by omission before any provider sees them.
// Other cells are unsupported: `mcp.servers` and `mcp.modes` have no reader, and
// `permissions.plugins` reaches req.Permissions.Plugins and stops there.
func resources(mcpOff, skillsOn bool) map[ResourceKind]map[ResourceMode]Support {
	mcpDisabled := unsupported("permissions.mcp.disabled is accepted and then dropped on this runtime")
	if mcpOff {
		mcpDisabled = native("all MCP servers silenced")
	}
	skillsEnabled := unsupported("skill directories are not loaded on this runtime")
	if skillsOn {
		skillsEnabled = native("--plugin-dir")
	}
	return map[ResourceKind]map[ResourceMode]Support{
		ResourceKindMCP: {
			ResourceEnabled:  unsupported("mcp.servers / mcp.modes have no reader: per-server enabling is not implemented"),
			ResourceDisabled: mcpDisabled,
		},
		ResourceKindSkills: {
			ResourceEnabled:  skillsEnabled,
			ResourceDisabled: native("omitted by ResourcePolicies.Enabled before any provider sees it"),
		},
		ResourceKindPlugins: {
			ResourceEnabled:  unsupported("permissions.plugins reaches req.Permissions.Plugins and has no reader beyond it"),
			ResourceDisabled: unsupported("permissions.plugins reaches req.Permissions.Plugins and has no reader beyond it"),
		},
	}
}

// claudeModes is the posture row shared by every claude transport: the enum was
// modelled on `claude --permission-mode`, so all six map one-to-one.
//
// setting is how that transport spells the knob — the CLI and cmux emit the
// literal flag, the SDK bridge sends a `permissionMode` initialize param — so
// Effects.Flag stays a truthful record of what is actually sent.
//
// emitsDefault distinguishes the two treatments of the unset posture: the
// anthropic cli omits the flag entirely, while cmux and the agent bridge send `default`
// explicitly. Claude 2.1.237 advertises `manual` in place of `default` yet still
// accepts `default`, so both spellings work today; the alias is undocumented and
// may not survive.
func claudeModes(setting string, emitsDefault bool) map[PermissionMode]Support {
	def := native("")
	if emitsDefault {
		def = nativeNote(setting+"default",
			"claude 2.1.237 advertises `manual` but still accepts `default` as an undocumented alias")
	}
	return map[PermissionMode]Support{
		PermissionDefault:     def,
		PermissionPlan:        native(setting + "plan"),
		PermissionAcceptEdits: native(setting + "acceptEdits"),
		PermissionAuto:        native(setting + "auto"),
		PermissionBypass:      native(setting + "bypassPermissions"),
		PermissionDontAsk:     native(setting + "dontAsk"),
	}
}

// codexModes is the approval-posture row for every Codex transport. Isolation
// is selected independently through sandbox.mode and sandbox.policy.
//
// Plan remains approximated because Codex has no plan-only execution mode.
func codexModes(suppressesEscalation bool) map[PermissionMode]Support {
	planNote := "codex has no plan-only approval posture; native isolation remains independently configured"
	if suppressesEscalation {
		planNote = "the app-server additionally refuses every escalation request while in plan mode"
	}
	return map[PermissionMode]Support{
		PermissionDefault: codexPosture("", "on-request", "codex has no default posture of its own; approval resolves to on-request"),
		PermissionPlan:    codexPosture("", "on-request", planNote),
		PermissionAcceptEdits: codexPosture("", "on-request",
			"codex cannot auto-approve edits independently from other approval requests"),
		PermissionAuto: codexPosture("", "on-request",
			"codex has no auto posture distinct from on-request"),
		PermissionBypass: codexPosture("", "never",
			"approval prompts are disabled without changing the configured sandbox isolation"),
		PermissionDontAsk: unsupported(
			"codex cannot preserve tool-level denial while disabling approval prompts"),
	}
}

// geminiModes is derived from geminiApprovalMode, which emits no flag at all for
// the default posture.
func geminiModes() map[PermissionMode]Support {
	const setting = "--approval-mode "
	return map[PermissionMode]Support{
		PermissionDefault:     native(""),
		PermissionPlan:        native(setting + "plan"),
		PermissionAcceptEdits: approxFlag(setting+"auto_edit", "gemini auto-approves edit tools rather than all edits"),
		PermissionAuto:        approxFlag(setting+"auto_edit", "gemini auto-approves edit tools rather than all edits"),
		// yolo auto-approves every tool call, which is exactly what
		// bypassPermissions asks for — the one gemini posture that matches outright.
		PermissionBypass:  native(setting + "yolo"),
		PermissionDontAsk: approxFlag(setting+"yolo", "yolo also grants write access, which dontAsk does not itself request"),
	}
}

// claudeBuiltinTools and the rest are the built-in vocabularies a per-tool
// policy may name. They are projected from the agent tool tables in
// agent_resources.go rather than restated here: the permission catalog serves
// those same tools with their groups and descriptions, and two hand-kept lists
// of the same names is how the catalog came to serve claude's tools to every
// runtime in the first place.
var (
	claudeBuiltinTools = agentToolNames(agentTools["claude"])
	codexBuiltinTools  = agentToolNames(agentTools["codex"])
	geminiBuiltinTools = agentToolNames(agentTools["gemini"])
)

// permissionCapabilities is the matrix. One row per provider×mode runtime; every
// runtime in AllRuntimes must appear, which TestPermissionCapabilitiesCoverEveryRuntime
// enforces so that adding one forces a decision here.
var permissionCapabilities = map[Runtime]PermissionCapabilities{
	// --- The api mode: genkit reads Permissions.Tools only to refuse an
	// unenforceable policy, and has no provider-native approval posture. Caller
	// tools are the one axis these can honour, and they honour it fully.
	RuntimeOf(Anthropic, ModeAPI): {
		Modes:        unsupportedModes(),
		ToolPolicies: toolPolicies(noToolFilter(), callerTools(), noToolFilter()),
		Resources:    resources(false, false),
	},
	RuntimeOf(OpenAI, ModeAPI): {
		Modes:        unsupportedModes(),
		ToolPolicies: toolPolicies(noToolFilter(), callerTools(), noToolFilter()),
		Resources:    resources(false, false),
	},
	RuntimeOf(Google, ModeAPI): {
		Modes:        unsupportedModes(),
		ToolPolicies: toolPolicies(noToolFilter(), callerTools(), noToolFilter()),
		Resources:    resources(false, false),
	},
	RuntimeOf(DeepSeek, ModeAPI): {
		Modes:        unsupportedModes(),
		ToolPolicies: toolPolicies(noToolFilter(), callerTools(), noToolFilter()),
		Resources:    resources(false, false),
	},

	// --- claude transports: the enum's native home.
	RuntimeOf(Anthropic, ModeCLI): {
		Modes:        claudeModes("--permission-mode ", false),
		ToolPolicies: toolPolicies(claudeAgentTools(), noToolFilter(), noToolFilter()),
		// --mcp-config {} --strict-mcp-config genuinely disables ambient MCP;
		// --plugin-dir carries permissions.skills. This is the only runtime that
		// honours either.
		Resources: resources(true, true),
		Tools:     claudeBuiltinTools,
	},
	RuntimeOf(Anthropic, ModeAgent): {
		Modes:        claudeModes("permissionMode=", true),
		ToolPolicies: toolPolicies(claudeAgentTools(), callerTools(), noToolFilter()),
		// The SDK bridge sends only the caller-tool server list, so an
		// mcp.disabled request never reaches ambient servers. prepareCallerTools
		// errors when caller tools and mcp.disabled are combined, which is a
		// compatibility refusal rather than enforcement.
		Resources: resources(false, false),
		Tools:     claudeBuiltinTools,
	},
	RuntimeOf(Anthropic, ModeCmux): {
		Modes:        claudeModes("--permission-mode ", true),
		ToolPolicies: toolPolicies(claudeAgentTools(), noToolFilter(), noToolFilter()),
		Resources:    resources(false, false),
		Tools:        claudeBuiltinTools,
	},

	// --- codex transports: posture is a sandbox/approval pair, never a mode.
	RuntimeOf(OpenAI, ModeCLI): {
		Modes:        codexModes(false),
		ToolPolicies: toolPolicies(noToolFilter(), noToolFilter(), noToolFilter()),
		Resources:    resources(false, false),
		Tools:        codexBuiltinTools,
	},
	RuntimeOf(OpenAI, ModeAgent): {
		Modes: codexModes(true),
		// The caller row is the point of the provenance dimension: the openai
		// agent has no tool filter of its own, yet a denied caller tool is simply
		// never registered, so the policy is fully enforced.
		ToolPolicies: toolPolicies(noToolFilter(), callerTools(), noToolFilter()),
		// codexThreadConfig sends an empty mcp_servers map when MCP is disabled.
		Resources: resources(true, false),
		Tools:     codexBuiltinTools,
	},
	RuntimeOf(OpenAI, ModeCmux): {
		Modes:        codexModes(false),
		ToolPolicies: toolPolicies(noToolFilter(), noToolFilter(), noToolFilter()),
		Resources:    resources(false, false),
		Tools:        codexBuiltinTools,
	},

	// --- gemini CLI: an approval-mode flag and nothing else. The provider
	// documents that it has no per-run MCP override.
	RuntimeOf(Google, ModeCLI): {
		Modes:        geminiModes(),
		ToolPolicies: toolPolicies(noToolFilter(), noToolFilter(), noToolFilter()),
		Resources:    resources(false, false),
		Tools:        geminiBuiltinTools,
	},
}
