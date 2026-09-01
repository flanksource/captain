package ai

import (
	"fmt"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// Which attachment types an adapter can carry is a per-mode provider capability
// (registry.ModeCapabilities.MediaTypes), not a table maintained here.

// AdapterInputMediaTypes returns the attachment types a runtime can carry.
func AdapterInputMediaTypes(p *api.ModelProvider, mode api.RuntimeMode) []string {
	if p == nil {
		return []string{}
	}
	return p.AdapterMediaTypes(mode)
}

func modelInputMediaTypes(p *api.ModelProvider, mode api.RuntimeMode, model string) []string {
	if p == nil {
		return []string{}
	}
	return p.MediaTypesFor(mode, model)
}

func clampInputMediaTypes(p *api.ModelProvider, mode api.RuntimeMode, modelTypes []string) []string {
	return registry.ClampMediaTypes(AdapterInputMediaTypes(p, mode), modelTypes)
}

func ValidateAttachmentCompatibility(models []api.Model, refs []api.AttachmentRef) error {
	if len(refs) == 0 {
		return nil
	}
	for _, model := range models {
		p, mode, err := model.Runtime()
		if err != nil {
			return fmt.Errorf("resolve attachment runtime for %s: %w", model.Name, err)
		}
		accepted := modelInputMediaTypes(p, mode, model.Name)
		for _, ref := range refs {
			if !registry.MediaTypeAccepted(accepted, ref.MediaType) {
				return fmt.Errorf("%s:%s does not accept %s attachments", mode, model.Name, ref.MediaType)
			}
		}
	}
	return nil
}
