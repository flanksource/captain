package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
)

type AIRuntimeResolveOptions struct {
	// Context is required once a perms selection is non-empty: the preset
	// catalog is discovered, and its database opened, under it.
	Context      context.Context
	Layers       []api.SpecLayer
	Saved        captainconfig.Config
	Cwd          string
	RequireModel bool
	// DefaultPerms is the host's permission-set selection (runtime preset ids
	// or names) when --perms is not passed.
	DefaultPerms []string
	// CatalogSources are the host's own embedded preset sources
	// (runtimeprofiles.NewEmbeddedSource) perms resolve against. They override
	// captain's built-ins and yield to database, user and repo presets.
	CatalogSources []runtimeprofiles.Source
}

func logRuntimeWarnings(warnings []string) {
	for _, warning := range warnings {
		log.Warnf("preflight: %s", warning)
	}
}

type AIRuntimeResolved struct {
	Request    ai.Request
	Config     ai.Config
	Resolution api.ResolvedSpec
	// Presets is the perms selection layered into Resolution, nil when none.
	// promptrun.RunMetadata records it the way prompt runs record presets.
	Presets *runtimeprofiles.PresetResolution
}

func resolveInvocation(options AIRuntimeOptions, layers []api.SpecLayer) (AIRuntimeResolved, error) {
	saved, err := loadSavedConfig()
	if err != nil {
		return AIRuntimeResolved{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return AIRuntimeResolved{}, fmt.Errorf("get working directory: %w", err)
	}
	// The commands calling resolveInvocation carry no context yet, so a
	// --perms selection resolves under a fresh root.
	return options.Resolve(AIRuntimeResolveOptions{Context: context.Background(), Layers: layers, Saved: saved, Cwd: cwd, RequireModel: true})
}

type AIRuntimeProjectOptions struct {
	Resolved api.ResolvedSpec
	Saved    captainconfig.Config
}

func (o AIRuntimeOptions) Project(options AIRuntimeProjectOptions) (AIRuntimeResolved, error) {
	selection, err := resolveSandboxSelection(sandboxSelectionOptions{Spec: options.Resolved.Spec, Saved: options.Saved.Sandbox})
	if err != nil {
		return AIRuntimeResolved{}, err
	}
	if descriptor, ok := registry.SandboxFor(selection.Kind); ok && options.Resolved.Spec.Mode != "" {
		if err := descriptor.ValidateMode(options.Resolved.Spec.Mode); err != nil {
			return AIRuntimeResolved{}, err
		}
	}
	cfg := configFromResolved(options.Resolved.Spec)
	cfg.APIKey = o.APIKey
	cfg.APIURL = strings.TrimSpace(o.APIURL)
	cfg.SchemaRepair = schemaRepairConfig(options.Saved.Prompts.SchemaRepair)
	cfg.SandboxSelection = sandboxSelectionConfig(selection, options.Resolved.Spec.Sandbox)
	return AIRuntimeResolved{Request: options.Resolved.Spec, Config: cfg, Resolution: options.Resolved}, nil
}

func (o AIRuntimeOptions) Resolve(options AIRuntimeResolveOptions) (AIRuntimeResolved, error) {
	request, err := o.requestSpec()
	if err != nil {
		return AIRuntimeResolved{}, err
	}
	perms, err := o.resolvePerms(options)
	if err != nil {
		return AIRuntimeResolved{}, err
	}
	// Perms sit between the caller's layers and the flags, so ties within a
	// scope go to the perms over the host and to the flags over the perms.
	layers := append([]api.SpecLayer(nil), options.Layers...)
	if perms != nil {
		layers = append(layers, perms.Layers...)
	}
	if len(request.Fields()) > 0 {
		layers = append(layers, api.RequestSpecLayer("CLI flags", request))
	}
	options.Layers = layers
	resolved, err := o.resolveAuthored(options)
	if err != nil {
		return AIRuntimeResolved{}, err
	}
	resolved.Presets = perms
	return resolved, nil
}

// resolvePerms selects --perms when it was passed, else the host's
// DefaultPerms, through the preset resolver `prompt run --preset` uses.
func (o AIRuntimeOptions) resolvePerms(options AIRuntimeResolveOptions) (*runtimeprofiles.PresetResolution, error) {
	explicit := o.Perms != nil
	refs := nonBlankRefs(options.DefaultPerms)
	if explicit {
		refs = nonBlankRefs(o.Perms)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if options.Context == nil {
		return nil, fmt.Errorf("perms %s: AIRuntimeResolveOptions.Context is required to resolve permission sets", strings.Join(refs, ","))
	}
	selection := runtimePresetSelection{Catalog: runtimeprofiles.DefaultCatalogOptions{
		Config: &options.Saved, Cwd: options.Cwd, Sources: options.CatalogSources,
	}}
	if explicit {
		selection.Requested, selection.RequestedSet = refs, true
	} else {
		selection.Default = refs
	}
	presets, warnings, err := selectRuntimePresets(options.Context, selection)
	if errors.Is(err, runtimeprofiles.ErrNotFound) {
		return nil, availablePermsError(options.Context, selection.Catalog, err)
	}
	if err != nil {
		return nil, fmt.Errorf("perms: %w", err)
	}
	logRuntimeWarnings(warnings)
	return presets, nil
}

func nonBlankRefs(refs []string) []string {
	var kept []string
	for _, ref := range refs {
		if ref = strings.TrimSpace(ref); ref != "" {
			kept = append(kept, ref)
		}
	}
	return kept
}

// availablePermsError extends an unknown-perm failure with every preset name
// the catalog offers, in precedence order.
func availablePermsError(ctx context.Context, options runtimeprofiles.DefaultCatalogOptions, cause error) error {
	catalog, err := buildRuntimeCatalog(ctx, options)
	if err != nil {
		return errors.Join(fmt.Errorf("perms: %w", cause), fmt.Errorf("list available perms: %w", err))
	}
	presets, err := catalog.ListPresets(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("perms: %w", cause), fmt.Errorf("list available perms: %w", err))
	}
	names := make([]string, 0, len(presets))
	for _, preset := range presets {
		names = append(names, preset.Name)
	}
	return fmt.Errorf("perms: %w; available perms: %s", cause, strings.Join(names, ", "))
}

func (o AIRuntimeOptions) resolveAuthored(options AIRuntimeResolveOptions) (AIRuntimeResolved, error) {
	resolved, err := api.ResolveSpecLayers(api.ResolveSpecOptions{
		Layers: options.Layers, Saved: &options.Saved.AI, RequireModel: options.RequireModel,
		Normalize: func(spec api.Spec) (api.SpecNormalization, error) {
			return o.Normalize(AIRuntimeNormalizeOptions{Spec: spec, Saved: options.Saved, Cwd: options.Cwd})
		},
	})
	if err != nil {
		return AIRuntimeResolved{}, err
	}
	return o.Project(AIRuntimeProjectOptions{Resolved: resolved, Saved: options.Saved})
}
