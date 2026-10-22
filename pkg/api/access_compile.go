package api

import (
	"fmt"
	"maps"
	"strings"
)

// AccessReport is what CompileAccess did for one runtime, for explain output,
// run warnings and tests.
type AccessReport struct {
	Runtime string        `json:"runtime"`
	Dialect AccessDialect `json:"dialect,omitempty"`
	State   *AccessState  `json:"state,omitempty"`
	// Unchanged marks an intent compiled in its own dialect, which passes
	// through byte-identical.
	Unchanged   bool           `json:"unchanged,omitempty"`
	DeniedTools []string       `json:"deniedTools,omitempty"`
	Sandbox     string         `json:"sandbox,omitempty"`
	Approval    string         `json:"approval,omitempty"`
	Mode        PermissionMode `json:"mode,omitempty"`
	MCPDisabled bool           `json:"mcpDisabled,omitempty"`
	// Overridden are authored settings the compiled posture replaced.
	Overridden []string `json:"overridden,omitempty"`
	// Equivalences are authored tool denies a runtime without a tool filter
	// cannot carry, accepted because the compiled posture already enforces them.
	Equivalences []string `json:"equivalences,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// agentAccessDialects is the native dialect of each agent family's runtimes.
var agentAccessDialects = map[string]AccessDialect{
	"claude": AccessDialectTools,
	"codex":  AccessDialectSandbox,
	"gemini": AccessDialectApproval,
}

// RuntimeAccessDialect is the dialect a runtime natively expresses access in;
// empty for the API mode, which runs no built-in tools.
func RuntimeAccessDialect(runtime Runtime) AccessDialect {
	p, ok := runtime.ModelProvider()
	if !ok || runtime.Mode.Kind() != "cli" {
		return ""
	}
	return agentAccessDialects[p.AgentName]
}

type accessCompiler func(runtime Runtime, spec *Spec, state *AccessState, report *AccessReport) error

var accessCompilers = map[AccessDialect]accessCompiler{
	AccessDialectTools:    compileToolsAccess,
	AccessDialectSandbox:  compileSandboxAccess,
	AccessDialectApproval: compileApprovalAccess,
}

// CompileAccess compiles the spec's effective access intent into the runtime's
// native dialect. It is pure: the input is never mutated, and the result shares
// no map or sandbox the compiler changed. With no read-only intent, or for an
// intent written in the runtime's own dialect, the spec is returned unchanged.
// A posture the runtime cannot reach is an error, never a quieter run.
func CompileAccess(runtime Runtime, spec Spec) (Spec, AccessReport, error) {
	state := spec.Permissions.EffectiveAccess()
	report := AccessReport{Runtime: runtime.String(), Dialect: RuntimeAccessDialect(runtime), State: state}
	if !state.ReadOnly() {
		return spec, report, nil
	}
	if spec.Permissions.AccessState == nil {
		if allowed := toolsAllowing(spec.Permissions.Tools, readOnlyDeniedAliases); len(allowed) > 0 {
			return Spec{}, report, fmt.Errorf("%s: permissions.access read-only contradicts permissions.tools %s: allow",
				runtime, strings.Join(allowed, ", "))
		}
	}
	compile, ok := accessCompilers[report.Dialect]
	if !ok {
		report.Notes = append(report.Notes, "no built-in file tools to restrict; caller tools stay governed by toolPolicy")
		return spec, report, nil
	}
	out := spec.cloneForAccess()
	if err := compile(runtime, &out, state, &report); err != nil {
		return Spec{}, report, fmt.Errorf("%s: permissions.access read-only: %w", runtime, err)
	}
	return out, report, nil
}

// cloneForAccess copies the parts of a spec an access compiler changes, so the
// caller's tool map and sandbox policy are never written through.
func (s Spec) cloneForAccess() Spec {
	s.Permissions.Tools = maps.Clone(s.Permissions.Tools)
	if s.Sandbox != nil {
		ref := *s.Sandbox
		if ref.Policy != nil {
			policy := *ref.Policy
			if policy.Filesystem != nil {
				fs := *policy.Filesystem
				policy.Filesystem = &fs
			}
			ref.Policy = &policy
		}
		s.Sandbox = &ref
	}
	return s
}

// compileToolsAccess is claude's dialect. An intent derived from claude's own
// tool rules passes through; one derived from a read-only sandbox gains the
// Edit/Write denies claude's Bash-only sandbox cannot supply.
func compileToolsAccess(runtime Runtime, spec *Spec, state *AccessState, report *AccessReport) error {
	p, _ := runtime.ModelProvider()
	vocabulary := AgentToolsFor(p, runtime.Mode)
	switch state.Dialect {
	case AccessDialectTools:
		report.Unchanged = true
		report.Notes = append(report.Notes, "the authored tool rules are this runtime's own dialect and are used as-is")
	case AccessDialectSandbox:
		denyAccessTools(spec, report, aliasCarriers(vocabulary, writeAliases))
		report.Notes = append(report.Notes, "the sandbox confines Bash only, so edit and write tools are denied; Bash stays under the authored sandbox")
	default:
		denyAccessTools(spec, report, append(aliasCarriers(vocabulary, readOnlyDeniedAliases), claudeSubAgentTools...))
		spec.Permissions.MCP.Disabled = true
		report.MCPDisabled = true
	}
	return nil
}

func denyAccessTools(spec *Spec, report *AccessReport, names []string) {
	if spec.Permissions.Tools == nil {
		spec.Permissions.Tools = Tools{}
	}
	for _, name := range names {
		if current, ok := spec.Permissions.Tools[name]; ok && current != ToolPolicyDeny {
			report.Overridden = append(report.Overridden, fmt.Sprintf("permissions.tools %s: %s", name, current))
		}
		spec.Permissions.Tools[name] = ToolPolicyDeny
		report.DeniedTools = append(report.DeniedTools, name)
	}
}

// compileSandboxAccess is codex's dialect: a read-only native sandbox, approval
// never so no broker can grant an escalation, and no third-party MCP. An
// intent derived from a read-only sandbox already has its sandbox and only
// gains approval never.
func compileSandboxAccess(runtime Runtime, spec *Spec, state *AccessState, report *AccessReport) error {
	cliPosture, err := codexCLIArgsPosture(runtime, spec.CLIArgs)
	if err != nil {
		return err
	}
	native := state.Dialect == AccessDialectSandbox
	if !cliPosture && !native {
		if err := confineCodexSandbox(spec); err != nil {
			return err
		}
	}
	if !cliPosture {
		codexNeverApproval(runtime, spec, report)
	}
	if !native {
		spec.Permissions.MCP.Disabled = true
		report.MCPDisabled = true
	}
	report.Sandbox, report.Approval = string(CodexSandboxReadOnly), string(CodexApprovalNever)
	stripEnforcedDenies(runtime, spec, report, "enforced by the read-only sandbox")
	return nil
}

// codexCLIArgsPosture reads cmux's raw codex flags. They bypass the sandbox
// translation entirely, so they must already say read-only and never; anything
// looser is refused rather than overridden behind the author's back.
func codexCLIArgsPosture(runtime Runtime, args map[string]any) (bool, error) {
	if runtime.Mode != ModeCmux || (args["sandbox"] == nil && args["askForApproval"] == nil) {
		return false, nil
	}
	sandbox, _ := args["sandbox"].(string)
	approval, _ := args["askForApproval"].(string)
	if sandbox == string(CodexSandboxReadOnly) && approval == string(CodexApprovalNever) {
		return true, nil
	}
	return false, fmt.Errorf("cliArgs sandbox=%v askForApproval=%v is looser than read-only; set sandbox read-only and askForApproval never, or remove them",
		args["sandbox"], args["askForApproval"])
}

func confineCodexSandbox(spec *Spec) error {
	ref := spec.Sandbox
	if ref == nil {
		spec.Sandbox = &SandboxRef{Mode: SandboxNative, Policy: &NativeSandboxPolicy{
			Filesystem: &SandboxFilesystemPolicy{Access: SandboxFilesystemReadOnly}}}
		return nil
	}
	if ref.Mode != SandboxNative {
		return fmt.Errorf("sandbox mode %q cannot confine the agent; read-only needs the native sandbox", ref.Mode)
	}
	if ref.Policy == nil {
		ref.Policy = &NativeSandboxPolicy{}
	}
	fs := ref.Policy.Filesystem
	switch {
	case fs == nil:
		ref.Policy.Filesystem = &SandboxFilesystemPolicy{Access: SandboxFilesystemReadOnly}
	case fs.Access == SandboxFilesystemWorkspaceWrite:
		return fmt.Errorf("sandbox.policy.filesystem.access workspace-write grants the writes read-only forbids")
	case len(fs.WritableRoots) > 0:
		return fmt.Errorf("sandbox.policy.filesystem.writableRoots grants the writes read-only forbids")
	default:
		fs.Access = SandboxFilesystemReadOnly
	}
	return nil
}

// codexNeverApproval sets the posture whose approval policy is never. The
// app-server's plan mode already refuses every escalation, so it is kept there.
func codexNeverApproval(runtime Runtime, spec *Spec, report *AccessReport) {
	mode := spec.Permissions.Mode
	if mode == PermissionPlan && runtime.Mode == ModeAgent {
		report.Mode = mode
		report.Equivalences = append(report.Equivalences, "permissions.mode plan: the app-server refuses every escalation request in plan mode")
		return
	}
	if mode != "" && mode != PermissionBypass {
		report.Overridden = append(report.Overridden, fmt.Sprintf("permissions.mode %s", mode))
	}
	spec.Permissions.Mode = PermissionBypass
	report.Mode = PermissionBypass
}

// compileApprovalAccess is gemini's dialect: the plan approval mode.
func compileApprovalAccess(runtime Runtime, spec *Spec, _ *AccessState, report *AccessReport) error {
	if mode := spec.Permissions.Mode; mode != "" && mode != PermissionPlan {
		report.Overridden = append(report.Overridden, fmt.Sprintf("permissions.mode %s", mode))
	}
	spec.Permissions.Mode = PermissionPlan
	report.Mode = PermissionPlan
	report.Notes = append(report.Notes, "plan mode is enforced by the gemini client, not an OS sandbox")
	stripEnforcedDenies(runtime, spec, report, "enforced by the plan approval mode")
	return nil
}

// stripEnforcedDenies removes the authored denies the compiled posture already
// enforces on a runtime with no tool filter, recording each as an equivalence.
// A key is removed only when every tool it names is enforced, so a residual
// deny the posture does not cover still reaches RequireToolPolicySupport.
func stripEnforcedDenies(runtime Runtime, spec *Spec, report *AccessReport, enforcedBy string) {
	p, _ := runtime.ModelProvider()
	vocabulary := AgentToolsFor(p, runtime.Mode)
	for _, key := range sortedKeys(spec.Permissions.Tools) {
		if spec.Permissions.Tools[key] != ToolPolicyDeny {
			continue
		}
		resolved, _ := Tools{key: ToolPolicyDeny}.resolveForRuntime(p, runtime.Mode)
		var reasons []string
		for _, rule := range sortedKeys(resolved) {
			reason, ok := accessEnforcedDeny(vocabulary, rule, enforcedBy)
			if !ok {
				reasons = nil
				break
			}
			reasons = append(reasons, resolved[rule].describe(rule)+": "+reason)
		}
		if len(reasons) > 0 {
			delete(spec.Permissions.Tools, key)
			report.Equivalences = append(report.Equivalences, reasons...)
		}
	}
}

// accessEnforcedDeny reports whether a read-only posture compiled for a runtime
// without a tool filter enforces a deny on rule: an edit or write tool always;
// a shell only where the runtime has no shell-free read path, because there
// the shell is how it reads and the read-only sandbox confines it.
func accessEnforcedDeny(vocabulary []AgentTool, rule, enforcedBy string) (string, bool) {
	base, _, _ := strings.Cut(rule, "(")
	tool, ok := vocabularyTool(vocabulary, base, false)
	switch {
	case !ok:
		return "", false
	case len(aliasCarriers([]AgentTool{tool}, writeAliases)) > 0:
		return enforcedBy, true
	case len(aliasCarriers([]AgentTool{tool}, []string{"shell"})) > 0 && len(aliasCarriers(vocabulary, []string{"read"})) == 0:
		return "kept as the read path, " + enforcedBy, true
	default:
		return "", false
	}
}
