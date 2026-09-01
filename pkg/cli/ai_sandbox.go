package cli

import (
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// resolveSandboxSelection applies the sandbox precedence chain — CLI flag >
// prompt frontmatter > global default > off — and resolves the winner against
// the backends configured in ~/.captain.yaml.
//
// Kinds whose execution seam is not wired yet fail loud here rather than
// resolving successfully and then silently running unsandboxed; each adapter
// removes itself from the guard when its wiring lands.
func resolveSandboxSelection(flagSelector string, ref *api.SandboxRef, defaults captainconfig.SandboxDefaults) (captainconfig.SandboxSelection, error) {
	selector := strings.TrimSpace(flagSelector)
	if selector == "" && ref != nil {
		selector = strings.TrimSpace(ref.Backend)
		if selector == "" {
			selector = string(ref.Mode)
		}
	}
	selection, err := defaults.Resolve(selector)
	if err != nil {
		return captainconfig.SandboxSelection{}, err
	}
	return selection, nil
}

// resolveRunSandbox resolves the sandbox for one run from the request's own ref
// — the prompt's `sandbox:` frontmatter, or a spec override already layered onto
// it — plus an optional operator-supplied selector.
func resolveRunSandbox(req *ai.Request, flagSelector string) (captainconfig.SandboxSelection, error) {
	return resolveSandboxSelection(flagSelector, req.Sandbox, loadSavedConfig().Sandbox)
}

// recordSandboxSelection writes the winning selection onto the run: the config
// the exec seam reads, and — when an operator named one explicitly — the request
// ref, so the serialized spec carries the choice the run was actually made with.
func recordSandboxSelection(req *ai.Request, cfg *ai.Config, selection captainconfig.SandboxSelection, flagSelector string) {
	if flagSelector != "" {
		ref := sandboxRefFromSelection(selection)
		if req.Sandbox != nil {
			ref.Policy = req.Sandbox.Policy
			ref.Agent = req.Sandbox.Agent
			ref.Dispatch = req.Sandbox.Dispatch
		}
		req.Sandbox = &ref
	}
	cfg.SandboxSelection = sandboxSelectionConfig(selection, req.Sandbox)
}

// applyRunSandbox is the transport-neutral seam: resolve, then record. Every
// entrypoint that builds a run must reach the sandbox through it or through its
// two halves. Resolution used to live only inside overlayCLI, so a run submitted
// over HTTP carried no selection at all — and the exec seam reads a nil
// selection as "unsandboxed", silently, because both fail-loud guards only fire
// once a selection exists.
func applyRunSandbox(req *ai.Request, cfg *ai.Config, flagSelector string) error {
	selection, err := resolveRunSandbox(req, flagSelector)
	if err != nil {
		return err
	}
	recordSandboxSelection(req, cfg, selection, flagSelector)
	return nil
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
