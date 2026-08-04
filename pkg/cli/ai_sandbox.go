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
	case registry.SandboxNone, registry.SandboxSRT:
		return selection, nil
	default:
		return captainconfig.SandboxSelection{}, fmt.Errorf(
			"sandbox kind %q is not wired to execution yet (supported today: %s, %s)",
			selection.Kind, registry.SandboxNone, registry.SandboxSRT)
	}
}
