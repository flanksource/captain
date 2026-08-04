package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// resolveSandboxSelection applies the sandbox precedence chain — CLI flag >
// prompt frontmatter > global default > none — and resolves the winner against
// the backends configured in ~/.captain.yaml.
//
// Kinds whose execution seam is not wired yet fail loud here rather than
// resolving successfully and then silently running unsandboxed; each adapter
// removes itself from the guard when its wiring lands.
func resolveSandboxSelection(flagSelector string, ref *api.SandboxRef, defaults captainconfig.SandboxDefaults) (captainconfig.SandboxSelection, error) {
	selector := strings.TrimSpace(flagSelector)
	if selector == "" && ref != nil {
		selector = strings.TrimSpace(ref.Backend)
	}
	selection, err := defaults.Resolve(selector)
	if err != nil {
		return captainconfig.SandboxSelection{}, err
	}
	switch selection.Kind {
	case registry.SandboxNone, registry.SandboxSRT, registry.SandboxContainer, registry.SandboxGitAgent:
		return selection, nil
	default:
		return captainconfig.SandboxSelection{}, fmt.Errorf(
			"sandbox kind %q is not wired to execution yet (supported today: %s, %s, %s, %s)",
			selection.Kind, registry.SandboxNone, registry.SandboxSRT, registry.SandboxContainer, registry.SandboxGitAgent)
	}
}

// sandboxForcedMode returns the single runtime mode a sandbox kind can serve,
// or "" when the kind serves several (or is none). Argv-wrapping adapters are
// CLI-only, so selecting one forces CLI mode the way --sandbox always has.
func sandboxForcedMode(kind registry.SandboxKind) registry.RuntimeMode {
	if kind == registry.SandboxNone {
		return ""
	}
	descriptor, ok := registry.SandboxFor(kind)
	if !ok || len(descriptor.Modes) != 1 {
		return ""
	}
	return descriptor.Modes[0]
}

// sandboxSelectionConfig projects a resolved selection onto the runtime
// config. "none" stays nil, so an unsandboxed run carries no selection at all
// and the exec seam's nil check keeps its meaning. ref, when present, carries
// the per-prompt agent pin and policy override (git-agent).
func sandboxSelectionConfig(selection captainconfig.SandboxSelection, ref *api.SandboxRef) *api.SandboxConfig {
	if selection.Kind == registry.SandboxNone {
		return nil
	}
	cfg := &api.SandboxConfig{Kind: selection.Kind, Name: selection.Name, Options: selection.Options}
	if ref != nil {
		cfg.Agent = ref.Agent
		cfg.Policy = ref.Policy
	}
	return cfg
}
