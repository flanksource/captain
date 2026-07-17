package aichat

import (
	"context"
	"fmt"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

// ToolSet couples executable Captain definitions with their optional canonical
// frontend catalog rows. When Catalog is empty it is projected from Definitions.
type ToolSet struct {
	Definitions []api.ToolDefinition
	Catalog     []aitools.ToolCatalogEntry
}

// ToolProvider loads an application or MCP tool set for the current request.
type ToolProvider interface {
	ToolSet(context.Context) (ToolSet, error)
}

type ToolProviderFunc func(context.Context) (ToolSet, error)

func (f ToolProviderFunc) ToolSet(ctx context.Context) (ToolSet, error) { return f(ctx) }

func StaticToolProvider(definitions []api.ToolDefinition) ToolProvider {
	return ToolProviderFunc(func(context.Context) (ToolSet, error) {
		return ToolSet{Definitions: append([]api.ToolDefinition(nil), definitions...)}, nil
	})
}

func CombineToolProviders(providers ...ToolProvider) ToolProvider {
	return ToolProviderFunc(func(ctx context.Context) (ToolSet, error) {
		combined := ToolSet{}
		seenDefinitions := map[string]bool{}
		seenCatalog := map[string]bool{}
		for i, provider := range providers {
			if provider == nil {
				continue
			}
			set, err := provider.ToolSet(ctx)
			if err != nil {
				return ToolSet{}, fmt.Errorf("load tool provider %d: %w", i+1, err)
			}
			if err := appendToolSet(&combined, set, "application", seenDefinitions, seenCatalog); err != nil {
				return ToolSet{}, err
			}
		}
		return combined, nil
	})
}

func (s *Service) loadTools(ctx context.Context) (ToolSet, error) {
	combined := ToolSet{}
	seenDefinitions := map[string]bool{}
	seenCatalog := map[string]bool{}
	for _, source := range []struct {
		name     string
		provider ToolProvider
	}{{name: "custom", provider: s.options.Tools}, {name: "mcp", provider: s.options.MCP}} {
		if source.provider == nil {
			continue
		}
		set, err := source.provider.ToolSet(ctx)
		if err != nil {
			return ToolSet{}, fmt.Errorf("load %s tools: %w", source.name, err)
		}
		if err := appendToolSet(&combined, set, source.name, seenDefinitions, seenCatalog); err != nil {
			return ToolSet{}, err
		}
	}
	return combined, nil
}

func appendToolSet(combined *ToolSet, set ToolSet, source string, seenDefinitions, seenCatalog map[string]bool) error {
	for _, definition := range set.Definitions {
		if definition.Name == "" {
			return fmt.Errorf("%s tool name is required", source)
		}
		if definition.Handler == nil {
			return fmt.Errorf("%s tool %q handler is required", source, definition.Name)
		}
		if seenDefinitions[definition.Name] {
			return fmt.Errorf("duplicate tool definition %q", definition.Name)
		}
		seenDefinitions[definition.Name] = true
		combined.Definitions = append(combined.Definitions, definition)
	}
	catalog := set.Catalog
	if len(catalog) == 0 {
		catalog = make([]aitools.ToolCatalogEntry, 0, len(set.Definitions))
		for _, definition := range set.Definitions {
			entry := aitools.CustomCatalogEntry(toolDefinitionForCatalog(definition), definition.Name, definition.InputSchema)
			entry.Source = source
			catalog = append(catalog, entry)
		}
	}
	for _, entry := range catalog {
		if entry.Name == "" {
			return fmt.Errorf("%s tool catalog name is required", source)
		}
		if seenCatalog[entry.Name] {
			return fmt.Errorf("duplicate tool catalog entry %q", entry.Name)
		}
		seenCatalog[entry.Name] = true
		combined.Catalog = append(combined.Catalog, entry)
	}
	return nil
}

func toolDefinitionForCatalog(definition api.ToolDefinition) aitools.ToolDefinition {
	return aitools.ToolDefinition{
		Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema,
		Group: definition.Group, Parent: definition.Parent, Icon: definition.Icon,
		DefaultPermission: definition.DefaultPermission, Strict: definition.Strict,
		ReadOnlyHint: definition.ReadOnlyHint, DestructiveHint: definition.DestructiveHint,
		IdempotentHint: definition.IdempotentHint, Annotations: definition.Annotations,
	}
}
