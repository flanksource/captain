package api

import (
	"encoding/json"
	"fmt"
)

// CodexSandboxTranslation is the exact Codex CLI/app-server projection of a
// provider-neutral sandbox reference.
type CodexSandboxTranslation struct {
	Sandbox  CodexSandbox
	Approval CodexApprovalPolicy
	// ApprovalsReviewer is who answers the approval requests Approval lets
	// through; empty inherits the user's Codex configuration.
	ApprovalsReviewer CodexApprovalsReviewer
	WorkspaceWrite    map[string]any
}

// CodexApprovalsReviewer is Codex's approvals_reviewer (app-server
// approvalsReviewer), per `codex app-server generate-ts`.
type CodexApprovalsReviewer string

const (
	CodexReviewerUser             CodexApprovalsReviewer = "user"
	CodexReviewerAutoReview       CodexApprovalsReviewer = "auto_review"
	CodexReviewerGuardianSubagent CodexApprovalsReviewer = "guardian_subagent"
)

// ConfigArgs returns deterministic dotted TOML overrides for Codex's
// workspace-write policy.
func (t CodexSandboxTranslation) ConfigArgs() []string {
	keys := []string{"network_access", "writable_roots", "exclude_slash_tmp", "exclude_tmpdir_env_var"}
	args := make([]string, 0, len(t.WorkspaceWrite))
	for _, key := range keys {
		value, ok := t.WorkspaceWrite[key]
		if !ok {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			panic(fmt.Sprintf("encode validated Codex sandbox field %s: %v", key, err))
		}
		args = append(args, "sandbox_workspace_write."+key+"="+string(encoded))
	}
	return args
}

// TranslateCodexSandbox maps the unified sandbox contract to Codex's native
// sandbox and approval flags plus workspace-write config.
//
// The isolation boundary comes from ref; whether Codex asks before acting comes
// from mode, which the caller reads off Permissions. The two are independent, so
// `sandbox: off` no longer forces never-ask — an unstated mode still resolves to
// on-request, which is the safe direction to default in.
func TranslateCodexSandbox(runtime Runtime, ref *SandboxRef, mode PermissionMode) (CodexSandboxTranslation, error) {
	if err := validatePermissionModeSupport(runtime, mode); err != nil {
		return CodexSandboxTranslation{}, err
	}
	posture := CodexSandboxTranslation{Approval: codexApproval(mode), ApprovalsReviewer: codexReviewer(mode)}
	if ref == nil {
		return posture, nil
	}
	if err := ref.Validate(); err != nil {
		return CodexSandboxTranslation{}, fmt.Errorf("%s sandbox: %w", runtime, err)
	}
	switch ref.Mode {
	case SandboxOff:
		posture.Sandbox = CodexSandboxDangerFull
		return posture, nil
	case SandboxDocker, SandboxGitAgent:
		if mode == PermissionPlan {
			return CodexSandboxTranslation{}, fmt.Errorf("permissions.mode plan is not supported by %s with %s sandbox mode", runtime, ref.Mode)
		}
		posture.Sandbox = CodexSandboxDangerFull
		return posture, nil
	case SandboxNative:
		return translateCodexNative(runtime, ref.Policy, mode, posture)
	default:
		return CodexSandboxTranslation{}, fmt.Errorf("%s sandbox mode %q is unsupported", runtime, ref.Mode)
	}
}

func translateCodexNative(
	runtime Runtime,
	policy *NativeSandboxPolicy,
	mode PermissionMode,
	translation CodexSandboxTranslation,
) (CodexSandboxTranslation, error) {
	translation.Sandbox = CodexSandboxReadOnly
	if policy == nil {
		return translation, nil
	}
	if path := unsupportedCodexPolicyGroup(policy); path != "" {
		return CodexSandboxTranslation{}, UnsupportedSandboxField(runtime, path)
	}
	if err := translateCodexFilesystem(runtime, policy.Filesystem, mode, &translation); err != nil {
		return CodexSandboxTranslation{}, err
	}
	if err := translateCodexNetwork(runtime, policy.Network, &translation); err != nil {
		return CodexSandboxTranslation{}, err
	}
	return translation, nil
}

func unsupportedCodexPolicyGroup(policy *NativeSandboxPolicy) string {
	switch {
	case policy.Required != nil:
		return "policy.required"
	case policy.Commands != nil:
		return "policy.commands"
	case policy.Credentials != nil:
		return "policy.credentials"
	case policy.Platform != nil:
		return "policy.platform"
	default:
		return ""
	}
}

func translateCodexFilesystem(
	runtime Runtime,
	filesystem *SandboxFilesystemPolicy,
	mode PermissionMode,
	translation *CodexSandboxTranslation,
) error {
	if filesystem == nil {
		return nil
	}
	switch {
	case len(filesystem.ReadableRoots) > 0:
		return UnsupportedSandboxField(runtime, "policy.filesystem.readableRoots")
	case len(filesystem.DeniedReadRoots) > 0:
		return UnsupportedSandboxField(runtime, "policy.filesystem.deniedReadRoots")
	case len(filesystem.DeniedWriteRoots) > 0:
		return UnsupportedSandboxField(runtime, "policy.filesystem.deniedWriteRoots")
	}
	if filesystem.Access == SandboxFilesystemWorkspaceWrite && mode != PermissionPlan {
		translation.Sandbox = CodexSandboxWorkspaceWrite
	}
	if len(filesystem.WritableRoots) > 0 {
		translation.workspaceWrite()["writable_roots"] = filesystem.WritableRoots
	}
	if filesystem.IncludeSystemTemp != nil {
		exclude := !*filesystem.IncludeSystemTemp
		translation.workspaceWrite()["exclude_slash_tmp"] = exclude
		translation.workspaceWrite()["exclude_tmpdir_env_var"] = exclude
	}
	return nil
}

func translateCodexNetwork(
	runtime Runtime,
	network *SandboxNetworkPolicy,
	translation *CodexSandboxTranslation,
) error {
	if network == nil {
		return nil
	}
	switch {
	case len(network.AllowedDomains) > 0:
		return UnsupportedSandboxField(runtime, "policy.network.allowedDomains")
	case len(network.DeniedDomains) > 0 || len(network.AllowedUnixSockets) > 0 || network.AllowAllUnixSockets != nil ||
		network.AllowLocalBinding != nil || len(network.AllowedMachServices) > 0 || network.HTTPProxyPort != nil || network.SOCKSProxyPort != nil:
		return UnsupportedSandboxField(runtime, "policy.network")
	case network.Access == SandboxNetworkRestricted:
		return UnsupportedSandboxField(runtime, "policy.network.access=restricted")
	}
	translation.workspaceWrite()["network_access"] = network.Access == SandboxNetworkUnrestricted
	return nil
}

func (t *CodexSandboxTranslation) workspaceWrite() map[string]any {
	if t.WorkspaceWrite == nil {
		t.WorkspaceWrite = map[string]any{}
	}
	return t.WorkspaceWrite
}

// validatePermissionModeSupport refuses a posture the backend cannot honour,
// rather than translating it into a weaker one the caller never asked for.
func validatePermissionModeSupport(runtime Runtime, mode PermissionMode) error {
	if mode == "" || PermissionCapabilitiesFor(runtime).ModeSupport(mode).Honoured() {
		return nil
	}
	return fmt.Errorf("permissions.mode %q is not supported by %s", mode, runtime)
}

func codexApproval(mode PermissionMode) CodexApprovalPolicy {
	switch mode {
	case PermissionBypass, PermissionDontAsk:
		return CodexApprovalNever
	case "", PermissionDefault, PermissionAcceptEdits, PermissionAuto, PermissionPlan:
		return CodexApprovalOnRequest
	default:
		panic(fmt.Sprintf("invalid validated sandbox approval %q", mode))
	}
}

// codexReviewer names the reviewer explicitly for every stated posture: Codex
// keeps a turn/start override for the rest of the thread, so a switch away from
// auto must say "user" rather than fall silent.
func codexReviewer(mode PermissionMode) CodexApprovalsReviewer {
	switch mode {
	case "":
		return ""
	case PermissionAuto:
		return CodexReviewerAutoReview
	default:
		return CodexReviewerUser
	}
}

// CodexPermissionMode recovers the posture a recorded Codex turn ran under from
// its approval policy, approvals reviewer, and collaboration mode — the inverse
// of the approval half of TranslateCodexSandbox. default and acceptEdits share
// one Codex posture, so both read back as default; a policy other than never
// (on-request, untrusted, granular) still asks.
func CodexPermissionMode(approval string, reviewer CodexApprovalsReviewer, collaboration string) PermissionMode {
	switch {
	case collaboration == "plan":
		return PermissionPlan
	case approval == "":
		return ""
	case approval == string(CodexApprovalNever):
		return PermissionBypass
	case reviewer == CodexReviewerAutoReview || reviewer == CodexReviewerGuardianSubagent:
		return PermissionAuto
	default:
		return PermissionDefault
	}
}
