package api

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var (
	readOnlyTools = Tools{"edit": ToolPolicyDeny, "write": ToolPolicyDeny, "shell": ToolPolicyDeny}
	readOnlyFS    = &SandboxRef{Mode: SandboxNative, Policy: &NativeSandboxPolicy{
		Filesystem: &SandboxFilesystemPolicy{Access: SandboxFilesystemReadOnly}}}
	claudeAgent = RuntimeOf(Anthropic, ModeAgent)
	codexAgent  = RuntimeOf(OpenAI, ModeAgent)
	codexCmux   = RuntimeOf(OpenAI, ModeCmux)
	geminiCLI   = RuntimeOf(Google, ModeCLI)
)

func explicitReadOnly(spec Spec) Spec {
	spec.Permissions.Access = AccessReadOnly
	return spec
}

func derived(spec Spec, dialect AccessDialect, source string) Spec {
	spec.Permissions.AccessState = &AccessState{Level: AccessReadOnly, Dialect: dialect, Source: source, Layer: "preset"}
	return spec
}

var _ = Describe("permissions.access", func() {
	DescribeTable("decodes, validates and round-trips",
		func(document string, want AccessLevel, wantErr string) {
			var spec Spec
			Expect(yaml.Unmarshal([]byte(document), &spec)).To(Succeed())
			err := spec.ValidateStructure()
			if wantErr != "" {
				Expect(err).To(MatchError(ContainSubstring(wantErr)))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(spec.Permissions.Access).To(Equal(want))
			encoded, err := json.Marshal(spec)
			Expect(err).NotTo(HaveOccurred())
			var again Spec
			Expect(json.Unmarshal(encoded, &again)).To(Succeed())
			Expect(again.Permissions.Access).To(Equal(want))
		},
		Entry("read-only", "permissions: {access: read-only}", AccessReadOnly, ""),
		Entry("full", "permissions: {access: full}", AccessFull, ""),
		Entry("unknown level", "permissions: {access: workspace-write}", AccessLevel(""), `invalid permissions access "workspace-write"`),
	)

	It("leaves an unset access out of the encoded spec", func() {
		encoded, err := json.Marshal(Spec{Permissions: Permissions{Mode: PermissionPlan}})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).NotTo(ContainSubstring("access"))
	})
})

var _ = Describe("ResolveAccessState", func() {
	layer := func(name string, spec Spec) SpecLayer {
		return SpecLayer{Name: name, Scope: SpecLayerContext, Spec: spec}
	}
	DescribeTable("canonicalizes each layer, then the later layer wins",
		func(layers []SpecLayer, want *AccessState) {
			Expect(ResolveAccessState(layers)).To(Equal(want))
		},
		Entry("no access anywhere", []SpecLayer{layer("a", Spec{Permissions: Permissions{Mode: PermissionAuto}})}, nil),
		Entry("explicit read-only",
			[]SpecLayer{layer("prompt", Spec{Permissions: Permissions{Access: AccessReadOnly}})},
			&AccessState{Level: AccessReadOnly, Dialect: AccessDialectPortable, Source: "permissions.access", Layer: "prompt"}),
		Entry("a preset read-only overridden by a request full",
			[]SpecLayer{layer("preset", Spec{Permissions: Permissions{Access: AccessReadOnly}}), layer("request", Spec{Permissions: Permissions{Access: AccessFull}})},
			&AccessState{Level: AccessFull, Dialect: AccessDialectPortable, Source: "permissions.access", Layer: "request"}),
		Entry("tool denies covering every edit, write and shell carrier",
			[]SpecLayer{layer("read-only", Spec{Permissions: Permissions{Tools: readOnlyTools}})},
			&AccessState{Level: AccessReadOnly, Dialect: AccessDialectTools, Source: "permissions.tools", Layer: "read-only"}),
		Entry("claude names that leave Monitor and MultiEdit reachable do not convert",
			[]SpecLayer{layer("a", Spec{Permissions: Permissions{Tools: Tools{"Bash": ToolPolicyDeny, "Edit": ToolPolicyDeny, "Write": ToolPolicyDeny}}})}, nil),
		Entry("a native read-only sandbox",
			[]SpecLayer{layer("box", Spec{Sandbox: readOnlyFS})},
			&AccessState{Level: AccessReadOnly, Dialect: AccessDialectSandbox, Source: "sandbox.policy.filesystem.access", Layer: "box"}),
		Entry("plan mode never converts",
			[]SpecLayer{layer("plan", Spec{Permissions: Permissions{Mode: PermissionPlan, Tools: Tools{"shell": ToolPolicyDeny}}})}, nil),
		Entry("a later shell allow lifts a derived read-only",
			[]SpecLayer{layer("preset", Spec{Permissions: Permissions{Tools: readOnlyTools}}), layer("request", Spec{Permissions: Permissions{Tools: Tools{"Bash": ToolPolicyAllow}}})},
			&AccessState{Level: AccessFull, Dialect: AccessDialectTools, Source: "permissions.tools Bash: allow", Layer: "request"}),
		Entry("a later sandbox off lifts a sandbox read-only",
			[]SpecLayer{layer("box", Spec{Sandbox: readOnlyFS}), layer("request", Spec{Sandbox: &SandboxRef{Mode: SandboxOff}})},
			&AccessState{Level: AccessFull, Dialect: AccessDialectSandbox, Source: "sandbox mode off", Layer: "request"}),
		Entry("a later layer that grants nothing keeps the intent",
			[]SpecLayer{layer("preset", Spec{Permissions: Permissions{Tools: readOnlyTools}}), layer("prompt", Spec{Permissions: Permissions{Tools: Tools{"Read": ToolPolicyAllow}}})},
			&AccessState{Level: AccessReadOnly, Dialect: AccessDialectTools, Source: "permissions.tools", Layer: "preset"}),
	)

	It("refuses read-only and a shell allow in the same layer", func() {
		_, err := ResolveAccessState([]SpecLayer{layer("prompt", Spec{Permissions: Permissions{Access: AccessReadOnly, Tools: Tools{"Bash": ToolPolicyAllow}}})})
		Expect(err).To(MatchError(ContainSubstring(`spec layer "prompt": permissions.access read-only contradicts permissions.tools Bash: allow`)))
	})

	It("records the state on the resolved spec and its permissions", func() {
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
			PromptSpecLayer("prompt", Spec{Model: Model{Name: "agent:sonnet"}, Permissions: Permissions{Tools: readOnlyTools}})}})
		Expect(err).NotTo(HaveOccurred())
		want := &AccessState{Level: AccessReadOnly, Dialect: AccessDialectTools, Source: "permissions.tools", Layer: "prompt"}
		Expect(resolved.Access).To(Equal(want))
		Expect(resolved.Spec.Permissions.AccessState).To(Equal(want))
	})
})

var _ = Describe("CompileAccess", func() {
	DescribeTable("compiles read-only to each runtime's native dialect",
		func(runtime Runtime, spec Spec, wantTools Tools, wantSandbox *SandboxRef, wantMode PermissionMode, wantMCP bool) {
			compiled, _, err := CompileAccess(runtime, spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(compiled.Permissions.Tools).To(Equal(wantTools))
			Expect(compiled.Sandbox).To(Equal(wantSandbox))
			Expect(compiled.Permissions.Mode).To(Equal(wantMode))
			Expect(compiled.Permissions.MCP.Disabled).To(Equal(wantMCP))
		},
		Entry("claude: every edit, write and shell carrier plus sub-agents, and no MCP",
			claudeAgent, explicitReadOnly(Spec{Sandbox: &SandboxRef{Mode: SandboxOff}}),
			Tools{"Bash": ToolPolicyDeny, "BashOutput": ToolPolicyDeny, "KillShell": ToolPolicyDeny, "Monitor": ToolPolicyDeny,
				"Edit": ToolPolicyDeny, "MultiEdit": ToolPolicyDeny, "NotebookEdit": ToolPolicyDeny, "Write": ToolPolicyDeny,
				"Agent": ToolPolicyDeny, "Task": ToolPolicyDeny, "Workflow": ToolPolicyDeny, "TeamCreate": ToolPolicyDeny, "TeamDelete": ToolPolicyDeny,
				"SendMessage": ToolPolicyDeny, "RemoteTrigger": ToolPolicyDeny, "CronCreate": ToolPolicyDeny, "CronDelete": ToolPolicyDeny,
				"EnterWorktree": ToolPolicyDeny, "ExitWorktree": ToolPolicyDeny},
			&SandboxRef{Mode: SandboxOff}, PermissionMode(""), true),
		Entry("claude: a tool-derived intent passes through untouched",
			claudeAgent, derived(Spec{Permissions: Permissions{Tools: readOnlyTools}}, AccessDialectTools, "permissions.tools"),
			readOnlyTools, (*SandboxRef)(nil), PermissionMode(""), false),
		Entry("claude: a sandbox-derived intent adds the edit and write denies its sandbox cannot",
			claudeAgent, derived(Spec{Sandbox: readOnlyFS}, AccessDialectSandbox, "sandbox.policy.filesystem.access"),
			Tools{"Edit": ToolPolicyDeny, "MultiEdit": ToolPolicyDeny, "NotebookEdit": ToolPolicyDeny, "Write": ToolPolicyDeny},
			readOnlyFS, PermissionMode(""), false),
		Entry("codex: native read-only sandbox, approval never and no MCP",
			codexAgent, explicitReadOnly(Spec{}), Tools(nil), readOnlyFS, PermissionBypass, true),
		Entry("codex: a tool-derived intent drops the denies the sandbox enforces",
			codexAgent, derived(Spec{Permissions: Permissions{Tools: readOnlyTools}}, AccessDialectTools, "permissions.tools"),
			Tools{}, readOnlyFS, PermissionBypass, true),
		Entry("codex: a sandbox-derived intent keeps its sandbox and only stops asking",
			codexAgent, derived(Spec{Sandbox: readOnlyFS}, AccessDialectSandbox, "sandbox.policy.filesystem.access"),
			Tools(nil), readOnlyFS, PermissionBypass, false),
		Entry("codex agent: plan already refuses every escalation and is kept",
			codexAgent, explicitReadOnly(Spec{Permissions: Permissions{Mode: PermissionPlan}}), Tools(nil), readOnlyFS, PermissionPlan, true),
		Entry("codex cmux: raw flags that already say read-only and never are kept",
			codexCmux, explicitReadOnly(Spec{CLIArgs: map[string]any{"sandbox": "read-only", "askForApproval": "never"}}),
			Tools(nil), (*SandboxRef)(nil), PermissionMode(""), true),
		Entry("gemini: the plan approval mode",
			geminiCLI, explicitReadOnly(Spec{Permissions: Permissions{Mode: PermissionBypass}}), Tools(nil), (*SandboxRef)(nil), PermissionPlan, false),
		Entry("api: nothing to restrict",
			RuntimeOf(Anthropic, ModeAPI), explicitReadOnly(Spec{}), Tools(nil), (*SandboxRef)(nil), PermissionMode(""), false),
	)

	DescribeTable("refuses a posture the runtime cannot reach",
		func(runtime Runtime, spec Spec, want string) {
			_, _, err := CompileAccess(runtime, explicitReadOnly(spec))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("codex with the sandbox off", codexAgent, Spec{Sandbox: &SandboxRef{Mode: SandboxOff}}, `sandbox mode "off" cannot confine the agent`),
		Entry("codex with a docker sandbox", codexAgent, Spec{Sandbox: &SandboxRef{Mode: SandboxDocker}}, `sandbox mode "docker" cannot confine the agent`),
		Entry("codex with a workspace-write sandbox", codexAgent, Spec{Sandbox: &SandboxRef{Mode: SandboxNative, Policy: &NativeSandboxPolicy{
			Filesystem: &SandboxFilesystemPolicy{Access: SandboxFilesystemWorkspaceWrite}}}}, "workspace-write grants the writes"),
		Entry("codex cmux with looser raw flags", codexCmux, Spec{CLIArgs: map[string]any{"sandbox": "workspace-write"}}, "cliArgs sandbox=workspace-write"),
		Entry("any runtime with a shell allow beside read-only", claudeAgent, Spec{Permissions: Permissions{Tools: Tools{"shell": ToolPolicyAllow}}},
			"contradicts permissions.tools"),
	)

	It("denies every claude tool that acts outside the agent's own process, on every claude transport", func() {
		for _, runtime := range []Runtime{claudeAgent, RuntimeOf(Anthropic, ModeCLI), RuntimeOf(Anthropic, ModeCmux)} {
			p, _ := runtime.ModelProvider()
			compiled, _, err := CompileAccess(runtime, explicitReadOnly(Spec{}))
			Expect(err).NotTo(HaveOccurred())
			tools, ignored := compiled.Permissions.Tools.ForRuntime(p, runtime.Mode)
			Expect(ignored).To(BeEmpty(), "every denied name must be one the runtime knows, or the deny is dropped")
			Expect(tools.DenyList()).To(ContainElements("SendMessage", "RemoteTrigger", "CronCreate", "TeamCreate", "EnterWorktree"))
			Expect(tools.DenyList()).NotTo(ContainElements("ListAgents", "CronList", "WebFetch", "WebSearch"), "reads stay allowed")
		}
	})

	It("never mutates the input spec", func() {
		input := derived(Spec{Permissions: Permissions{Tools: Tools{"edit": ToolPolicyDeny, "write": ToolPolicyDeny, "shell": ToolPolicyDeny, "Read": ToolPolicyAllow}},
			Sandbox: &SandboxRef{Mode: SandboxNative, Policy: &NativeSandboxPolicy{Filesystem: &SandboxFilesystemPolicy{}}}},
			AccessDialectTools, "permissions.tools")
		before, err := json.Marshal(input)
		Expect(err).NotTo(HaveOccurred())
		for _, runtime := range []Runtime{claudeAgent, codexAgent, geminiCLI} {
			_, _, _ = CompileAccess(runtime, input)
		}
		after, err := json.Marshal(input)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(after)).To(Equal(string(before)))
		Expect(input.Sandbox.Policy.Filesystem.Access).To(BeEmpty())
	})

	It("reports the codex shell as an explicit equivalence", func() {
		_, report, err := CompileAccess(codexAgent, derived(Spec{Permissions: Permissions{Tools: Tools{"Bash": ToolPolicyDeny}}}, AccessDialectTools, "permissions.tools"))
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Equivalences).To(ConsistOf(
			"exec (from Bash): kept as the read path, enforced by the read-only sandbox",
			"exec_command (from Bash): kept as the read path, enforced by the read-only sandbox",
			"shell (from Bash): kept as the read path, enforced by the read-only sandbox",
			"write_stdin (from Bash): kept as the read path, enforced by the read-only sandbox",
		))
	})

	DescribeTable("reports how codex's execpolicy rules are kept out",
		func(runtime Runtime, want string) {
			GinkgoT().Setenv("XDG_CONFIG_HOME", "/config")
			_, report, err := CompileAccess(runtime, explicitReadOnly(Spec{}))
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ExecPolicy).To(ContainSubstring(want))
		},
		Entry("codex exec ignores them", RuntimeOf(OpenAI, ModeCLI), "--ignore-rules"),
		Entry("the app-server runs in the isolated home", codexAgent, "CODEX_HOME=/config/captain/codex-read-only"),
		Entry("the cmux TUI runs in the isolated home", codexCmux, "CODEX_HOME=/config/captain/codex-read-only"),
	)

	It("agrees with the matrix's codex cell and the sandbox translator", func() {
		compiled, _, err := CompileAccess(codexAgent, explicitReadOnly(Spec{}))
		Expect(err).NotTo(HaveOccurred())
		translation, err := TranslateCodexSandbox(codexAgent, compiled.Sandbox, compiled.Permissions.Mode)
		Expect(err).NotTo(HaveOccurred())
		cell := PermissionCapabilitiesFor(codexAgent).AccessSupport(AccessReadOnly)
		Expect(string(translation.Sandbox)).To(Equal(cell.Effects.Sandbox))
		Expect(string(translation.Approval)).To(Equal(cell.Effects.Approval))
	})
})

var _ = Describe("ConvertToAccess", func() {
	DescribeTable("rewrites a dialect only when the portable form is equivalent or tighter",
		func(spec Spec, want ConversionResult) {
			Expect(ConvertToAccess(spec)).To(Equal(want))
		},
		Entry("the read-only preset keeps its web denies as residuals",
			Spec{Permissions: Permissions{Tools: Tools{"edit": ToolPolicyDeny, "write": ToolPolicyDeny, "shell": ToolPolicyDeny,
				"web_search": ToolPolicyDeny, "web_fetch": ToolPolicyDeny}}},
			ConversionResult{Convertible: true, Dialect: AccessDialectTools,
				Permissions: Permissions{Access: AccessReadOnly, Tools: Tools{"web_fetch": ToolPolicyDeny, "web_search": ToolPolicyDeny}},
				Residual:    []string{"permissions.tools web_fetch: deny", "permissions.tools web_search: deny"},
				Dropped:     []string{"permissions.tools edit: deny", "permissions.tools shell: deny", "permissions.tools write: deny"}}),
		Entry("a native read-only sandbox with approval never",
			Spec{Permissions: Permissions{Mode: PermissionBypass}, Sandbox: readOnlyFS},
			ConversionResult{Convertible: true, Dialect: AccessDialectSandbox,
				Permissions: Permissions{Access: AccessReadOnly}, Sandbox: &SandboxRef{Mode: SandboxNative},
				Dropped: []string{"permissions.mode bypassPermissions", "sandbox.policy.filesystem.access read-only"}}),
		Entry("plan mode permits the plan-file write",
			Spec{Permissions: Permissions{Mode: PermissionPlan, Tools: Tools{"read": ToolPolicyAllow, "shell": ToolPolicyDeny}}},
			ConversionResult{Reason: "permissions.mode plan permits the plan-file write, so it is not read-only"}),
		Entry("a read-only sandbox that a broker could still escalate",
			Spec{Sandbox: readOnlyFS},
			ConversionResult{Reason: "a read-only sandbox still lets a broker approve escalated writes; add permissions.mode bypassPermissions (approval never) to convert it"}),
	)
})

var _ = Describe("RequireToolPolicySupport under read-only", func() {
	DescribeTable("accepts only the denies the compiled posture enforces",
		func(runtime Runtime, tools Tools, want string) {
			p, _ := runtime.ModelProvider()
			err := RequireToolPolicySupport(p, runtime.Mode, Permissions{Access: AccessReadOnly, Tools: tools})
			if want == "" {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("codex: edit, write and its shell read path", codexAgent, readOnlyTools, ""),
		Entry("codex: web search is still refused", codexAgent, Tools{"web_search": ToolPolicyDeny}, "web_search"),
		Entry("gemini: edit and write", geminiCLI, Tools{"edit": ToolPolicyDeny, "write": ToolPolicyDeny}, ""),
		Entry("gemini: the shell is not its read path", geminiCLI, Tools{"shell": ToolPolicyDeny}, "run_shell_command"),
	)

	It("suggests the portable form when the rules already say read-only", func() {
		p, _ := codexAgent.ModelProvider()
		err := RequireToolPolicySupport(p, codexAgent.Mode, Permissions{Tools: readOnlyTools})
		Expect(err).To(MatchError(ContainSubstring("permissions: {access: read-only}")))
	})
})
