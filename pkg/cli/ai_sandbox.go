package cli

import (
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

type sandboxSelectionOptions struct {
	Selector string
	Spec     api.Spec
	Saved    captainconfig.SandboxDefaults
}

func resolveSandboxSelection(options sandboxSelectionOptions) (captainconfig.SandboxSelection, error) {
	selector := strings.TrimSpace(options.Selector)
	ref := options.Spec.Sandbox
	if selector == "" && ref == nil && options.Spec.Fields().Has("/sandbox") {
		return captainconfig.SandboxSelection{Kind: registry.SandboxOff}, nil
	}
	if selector == "" && ref != nil {
		selector = strings.TrimSpace(ref.Backend)
		if selector == "" {
			selector = string(ref.Mode)
		}
	}
	selection, err := options.Saved.Resolve(selector)
	if err != nil {
		return captainconfig.SandboxSelection{}, err
	}
	return selection, nil
}

// sandboxForcedMode returns the single runtime mode a sandbox kind can serve,
// or "" when the kind serves several (or is off/native). Argv-wrapping adapters are
// CLI-only, so selecting one forces CLI mode the way --sandbox always has.
func sandboxForcedMode(kind registry.SandboxKind) registry.RuntimeMode {
	if kind == registry.SandboxOff || kind == registry.SandboxNative {
		return ""
	}
	descriptor, ok := registry.SandboxFor(kind)
	if !ok || len(descriptor.Modes) != 1 {
		return ""
	}
	return descriptor.Modes[0]
}

// sandboxSelectionConfig projects a resolved selection onto the runtime
// config. Off and Native stay nil because they do not wrap or relocate the
// provider process. ref, when present, carries
// the per-prompt agent pin and policy override (git-agent).
func sandboxSelectionConfig(selection captainconfig.SandboxSelection, ref *api.SandboxRef) *api.SandboxConfig {
	if selection.Kind == registry.SandboxOff || selection.Kind == registry.SandboxNative {
		return nil
	}
	cfg := &api.SandboxConfig{Kind: selection.Kind, Name: selection.Name, Options: selection.Options}
	if ref != nil {
		cfg.Agent = ref.Agent
		cfg.Dispatch = ref.Dispatch
	}
	return cfg
}

func sandboxRefFromSelection(selection captainconfig.SandboxSelection) api.SandboxRef {
	ref := api.SandboxRef{Mode: selection.Kind}
	if selection.Name != "" {
		ref.Backend = selection.Name
	}
	return ref
}
