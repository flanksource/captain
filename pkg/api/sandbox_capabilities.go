package api

// RuntimeSandboxCapabilities is the exact unified surface one runtime can
// translate. Clients render these fields; translators reject everything else.
type RuntimeSandboxCapabilities struct {
	Modes         []SandboxKind    `json:"modes"`
	ApprovalModes []PermissionMode `json:"approvalModes,omitempty"`
	NativeFields  []string         `json:"nativeFields,omitempty"`
}

var claudeNativeSandboxFields = []string{
	"sandbox.policy.required",
	"sandbox.policy.filesystem.access",
	"sandbox.policy.filesystem.writableRoots",
	"sandbox.policy.filesystem.readableRoots",
	"sandbox.policy.filesystem.deniedReadRoots",
	"sandbox.policy.filesystem.deniedWriteRoots",
	"sandbox.policy.network.access",
	"sandbox.policy.network.allowedDomains",
	"sandbox.policy.network.deniedDomains",
	"sandbox.policy.network.allowedUnixSockets",
	"sandbox.policy.network.allowAllUnixSockets",
	"sandbox.policy.network.allowLocalBinding",
	"sandbox.policy.network.allowedMachServices",
	"sandbox.policy.network.httpProxyPort",
	"sandbox.policy.network.socksProxyPort",
	"sandbox.policy.commands.excludedFromSandbox",
	"sandbox.policy.commands.allowUnsandboxed",
	"sandbox.policy.credentials.deniedFiles",
	"sandbox.policy.platform.allowAppleEvents",
	"sandbox.policy.platform.weakerNestedIsolation",
	"sandbox.policy.platform.weakerNetworkIsolation",
}

var codexNativeSandboxFields = []string{
	"sandbox.policy.filesystem.access",
	"sandbox.policy.filesystem.writableRoots",
	"sandbox.policy.filesystem.includeSystemTemp",
	"sandbox.policy.network.access",
}

// RuntimeSandboxCapabilitiesFor derives public modes from the adapter/runtime
// matrix and narrows Native to providers with a faithful translator.
func RuntimeSandboxCapabilitiesFor(p *ModelProvider, mode RuntimeMode) RuntimeSandboxCapabilities {
	if p == nil {
		return RuntimeSandboxCapabilities{Modes: []SandboxKind{SandboxOff}}
	}
	if _, known := p.Caps(mode); !known {
		return RuntimeSandboxCapabilities{Modes: []SandboxKind{SandboxOff}}
	}
	capabilities := RuntimeSandboxCapabilities{}
	for _, descriptor := range AllSandboxes() {
		if !descriptor.SupportsMode(mode) || !runtimeSupportsSandboxKind(p, mode, descriptor.Kind) {
			continue
		}
		capabilities.Modes = append(capabilities.Modes, descriptor.Kind)
	}
	if len(capabilities.Modes) == 0 {
		capabilities.Modes = []SandboxKind{SandboxOff}
	}
	if len(capabilities.Modes) > 1 {
		capabilities.ApprovalModes = SupportedPermissionModes(p, mode)
	}
	if containsSandboxMode(capabilities.Modes, SandboxNative) {
		switch p {
		case Anthropic:
			capabilities.NativeFields = append([]string(nil), claudeNativeSandboxFields...)
		case OpenAI:
			capabilities.NativeFields = append([]string(nil), codexNativeSandboxFields...)
		}
	}
	return capabilities
}

// runtimeSupportsSandboxKind reports whether a runtime has a faithful native
// translator. Only the local Claude and Codex transports do — the API mode has
// no process to isolate.
func runtimeSupportsSandboxKind(p *ModelProvider, mode RuntimeMode, kind SandboxKind) bool {
	if kind != SandboxNative {
		return true
	}
	return mode != ModeAPI && (p == Anthropic || p == OpenAI)
}

func containsSandboxMode(modes []SandboxKind, target SandboxKind) bool {
	for _, mode := range modes {
		if mode == target {
			return true
		}
	}
	return false
}
