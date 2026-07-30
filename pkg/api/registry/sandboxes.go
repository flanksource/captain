package registry

// The sandbox adapter descriptors.
//
// Mode support is declared from the seam that actually exists, not from what an
// adapter could plausibly reach. Argv-wrapping adapters serve ModeCLI only,
// because startCLIStream (pkg/ai/provider/cli.go) is the single exec site they
// hook: the agent SDK modes spawn their subprocess inside vendor code, and
// ModeAPI is an in-process HTTP call with no argv at all. Widening a Modes list
// means adding the wrapping seam for that mode first.
var (
	NoSandbox = &Sandbox{
		Kind:         SandboxNone,
		Description:  "Run the agent directly on the host, unconfined",
		Capabilities: nil,
		Modes:        []RuntimeMode{ModeAPI, ModeCLI, ModeAgent, ModeCmux},
	}

	SRTSandbox = &Sandbox{
		Kind:         SandboxSRT,
		Description:  "Confine the agent with sandbox-runtime (filesystem and network policy)",
		Capabilities: []SandboxCapability{CapabilityWrapCommand, CapabilityEgressProxy},
		Modes:        []RuntimeMode{ModeCLI},
	}

	ContainerSandbox = &Sandbox{
		Kind:         SandboxContainer,
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
)

// AllSandboxes lists every sandbox adapter in canonical order — the single
// source of truth behind SandboxFor, ParseSandboxKind, and the help/error
// strings. "none" is first because it is the default.
func AllSandboxes() []*Sandbox {
	return []*Sandbox{NoSandbox, SRTSandbox, ContainerSandbox, GitAgentSandbox}
}
