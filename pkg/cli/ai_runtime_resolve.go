package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

type AIRuntimeResolveOptions struct {
	Layers       []api.SpecLayer
	Saved        captainconfig.Config
	Cwd          string
	RequireModel bool
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
	return options.Resolve(AIRuntimeResolveOptions{Layers: layers, Saved: saved, Cwd: cwd, RequireModel: true})
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
	layers := append([]api.SpecLayer(nil), options.Layers...)
	if len(request.Fields()) > 0 {
		layers = append(layers, api.RequestSpecLayer("CLI flags", request))
	}
	options.Layers = layers
	return o.resolveAuthored(options)
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
