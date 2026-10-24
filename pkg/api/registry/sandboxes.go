package registry

// The sandbox adapter descriptors.
//
// Mode support is declared from the seam that actually exists, not from what an
// adapter could plausibly reach. Argv-wrapping adapters serve ModeCLI only,
// because startCLIStream (pkg/ai/provider/cli.go) is the single exec site they
// hook: the agent SDK modes spawn their subprocess inside vendor code, and
// ModeAPI is an in-process HTTP call with no argv at all. Widening a Modes list
// means adding the wrapping seam for that mode first.
//
// ToolPlacements follow the same rule. Off and native have no wrapper a tool
// could run inside (native is enforced by the provider itself), so their tools
// run on the host. Docker declares none: its container cannot reach the
// host-side caller-tool endpoint, so no inline tool would be callable. Git
// Agent runs remotely, with no route back to tools on this host.
var (
	OffSandbox = &Sandbox{
		Kind:           SandboxOff,
		Description:    "Disable provider filesystem/network isolation and built-in approval prompts",
		Capabilities:   nil,
		Modes:          []RuntimeMode{ModeAPI, ModeCLI, ModeAgent, ModeCmux},
		ToolPlacements: []ToolPlacement{ToolPlacementHost},
	}

	NativeSandbox = &Sandbox{
		Kind:           SandboxNative,
		Description:    "Translate one Captain policy into the provider's native sandbox",
		Capabilities:   nil,
		Modes:          []RuntimeMode{ModeCLI, ModeAgent, ModeCmux},
		ToolPlacements: []ToolPlacement{ToolPlacementHost},
	}

	DockerSandbox = &Sandbox{
		Kind:         SandboxDocker,
		Description:  "Run the agent inside a container image built from the configured presets",
		Capabilities: []SandboxCapability{CapabilityWrapCommand},
		Modes:        []RuntimeMode{ModeCLI},
	}

	GitAgentSandbox = &Sandbox{
		Kind:        SandboxGitAgent,
		Description: "Relocate the run onto an enrolled remote agent over git",
		Capabilities: []SandboxCapability{
			CapabilityRemoteExec,
			CapabilityIsolateWorkspace,
			CapabilityEgressProxy,
		},
		// No ModeAPI: the adapter's contract is that the agent's work comes back
		// as commits, and a direct API call produces no working-tree change to
		// push.
		Modes: []RuntimeMode{ModeCLI, ModeAgent, ModeCmux},
	}

	internalSRTSandbox = &Sandbox{
		Kind:         SandboxSRTInternal,
		Description:  "Confine Git Agent receive hooks with sandbox-runtime",
		Capabilities: []SandboxCapability{CapabilityWrapCommand},
		Modes:        []RuntimeMode{ModeCLI},
	}
)

// AllSandboxes lists every sandbox adapter in canonical order — the single
// source of truth behind SandboxFor, ParseSandboxKind, and the help/error
// strings. "none" is first because it is the default.
func AllSandboxes() []*Sandbox {
	return []*Sandbox{OffSandbox, NativeSandbox, DockerSandbox, GitAgentSandbox}
}
