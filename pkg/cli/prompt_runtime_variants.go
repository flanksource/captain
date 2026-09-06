package cli

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

type promptRuntimeOptions struct {
	Models  []api.Model
	Layers  []api.SpecLayer
	Options AIRuntimeOptions
	Saved   captainconfig.Config
	Cwd     string
	CLI     bool
}

func resolvePromptRuntimes(options promptRuntimeOptions) ([]AIRuntimeResolved, error) {
	var variants []AIRuntimeResolved
	var models []api.Model
	seen := map[string]bool{}
	for i, runtime := range options.Models {
		candidates, err := promptRuntimeLayers(i, runtime)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			layers := append(append([]api.SpecLayer(nil), options.Layers...), candidate)
			result, err := options.Options.resolveAuthored(AIRuntimeResolveOptions{Layers: layers, Saved: options.Saved, Cwd: options.Cwd, RequireModel: true})
			if err != nil {
				return nil, fmt.Errorf("%s: %w", candidate.Name, err)
			}
			key := result.Request.RuntimeKey()
			if options.CLI && seen[key] {
				continue
			}
			seen[key] = true
			variants = append(variants, result)
			models = append(models, result.Request.Model)
		}
	}
	if len(models) > 0 && (len(models) != 1 || !options.CLI) {
		if err := validatePromptRuntimes(models); err != nil {
			return nil, err
		}
	}
	return variants, nil
}

func promptRuntimeLayers(index int, runtime api.Model) ([]api.SpecLayer, error) {
	name := fmt.Sprintf("runtime %d", index+1)
	selector := strings.TrimSpace(runtime.Name)
	if strings.HasPrefix(selector, "*:") {
		models, err := registry.ParseModelMulti(selector, registry.ParseOptions{})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		layers := make([]api.SpecLayer, len(models))
		for i, model := range models {
			candidate := runtime
			candidate.Name = runtimeSelector(model)
			layers[i] = api.RequestSpecLayer(name+" ("+selector+")", api.Spec{Model: candidate})
		}
		return layers, nil
	}
	prefix := strings.TrimSuffix(selector, ":")
	if mode, valid := registry.ParseRuntimeMode(prefix); valid && selector != "" {
		runtime.Name, runtime.Mode = "", mode
		runtime.Explicit = runtime.Explicit.Clone()
		delete(runtime.Explicit, "/model")
	} else if strings.HasSuffix(selector, ":") {
		return nil, fmt.Errorf("%s: invalid runtime mode %q", name, prefix)
	}
	return []api.SpecLayer{api.RequestSpecLayer(name, api.Spec{Model: runtime})}, nil
}

func (rendered PromptRenderResult) validateVariants() error {
	if len(rendered.variants) != len(rendered.Runtimes) {
		return fmt.Errorf("prompt runtime variants are not prepared: got %d resolved candidates for %d runtimes", len(rendered.variants), len(rendered.Runtimes))
	}
	return nil
}

func renderVariant(rendered PromptRenderResult, variant AIRuntimeResolved) PromptRenderResult {
	out := rendered
	out.Input, out.Config, out.Resolution = variant.Request, variant.Config, variant.Resolution
	if !reflect.DeepEqual(out.Input.Prompt.Attachments, rendered.Input.Prompt.Attachments) {
		out.Input.Prompt.Attachments = slices.Clone(rendered.Input.Prompt.Attachments)
		out.Resolution.Spec = out.Input
		out.Resolution.Provenance = maps.Clone(out.Resolution.Provenance)
		if out.Resolution.Provenance == nil {
			out.Resolution.Provenance = map[string]api.FieldProvenance{}
		}
		path := "/prompt/attachments"
		origin := out.Resolution.Provenance[path]
		origin.NormalizedBy = &api.FieldSource{Kind: api.FieldSourceContext, Name: "attachment preparation", Key: path}
		out.Resolution.Provenance[path] = origin
	}
	out.Model = out.Config.Model.Name
	out.Provider = providerName(out.Config.Model.Provider)
	out.Mode = string(out.Config.Model.Mode)
	out.Runtimes, out.variants = nil, nil
	return out
}
