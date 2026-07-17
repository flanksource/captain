package ai

import (
	"fmt"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// Which attachment types an adapter can carry is a per-mode provider capability
// (registry.ModeCapabilities.MediaTypes), not a table maintained here.

// AdapterInputMediaTypes returns the attachment types a backend can carry.
func AdapterInputMediaTypes(backend api.Backend) []string {
	p, mode, ok := registry.ProviderFor(backend)
	if !ok {
		return []string{}
	}
	return p.AdapterMediaTypes(mode)
}

func modelInputMediaTypes(backend api.Backend, model string) []string {
	p, mode, ok := registry.ProviderFor(backend)
	if !ok {
		return []string{}
	}
	return p.MediaTypesFor(mode, model)
}

func clampInputMediaTypes(backend api.Backend, modelTypes []string) []string {
	return registry.ClampMediaTypes(AdapterInputMediaTypes(backend), modelTypes)
}

func ValidateAttachmentCompatibility(models []api.Model, refs []api.AttachmentRef) error {
	if len(refs) == 0 {
		return nil
	}
	for _, model := range models {
		backend, err := model.ResolveBackend()
		if err != nil {
			return fmt.Errorf("resolve attachment backend for %s: %w", model.Name, err)
		}
		accepted := modelInputMediaTypes(backend, model.Name)
		for _, ref := range refs {
			if !registry.MediaTypeAccepted(accepted, ref.MediaType) {
				return fmt.Errorf("%s:%s does not accept %s attachments", backend, model.Name, ref.MediaType)
			}
		}
	}
	return nil
}
