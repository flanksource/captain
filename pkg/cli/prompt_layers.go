package cli

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
)

const renderRequestLayer = "render request"

type runtimePresetSelection struct {
	Requested         []string
	RequestedSet      bool
	Pin               []string
	PinSet            bool
	DeprecatedRequest string
	DeprecatedPin     string
	Config            *captainconfig.Config
}

func selectRuntimePresets(ctx context.Context, options runtimePresetSelection) (*runtimeprofiles.PresetResolution, []string, error) {
	resolver := runtimeprofiles.NewResolver(func(ctx context.Context) (*runtimeprofiles.Catalog, error) {
		return buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{Config: options.Config})
	})
	result, err := resolver.Layers(ctx, runtimeprofiles.ResolveOptions{
		RequestedPresets: options.Requested, RequestedPresetsSet: options.RequestedSet,
		PinnedPresets: options.Pin, PinnedPresetsSet: options.PinSet,
		RequestedProfile: options.DeprecatedRequest, PinnedProfile: options.DeprecatedPin,
	})
	if err != nil {
		return nil, nil, err
	}
	return result.Presets, result.Warnings, nil
}

// promptLayers assembles authored preset, prompt and request layers. Captain's
// final resolution expands the effective model selector while retaining the raw trace.
func promptLayers(presets *runtimeprofiles.PresetResolution, source string, frontmatter ai.Request, user *api.Spec) ([]api.SpecLayer, error) {
	var layers []api.SpecLayer
	if presets != nil {
		layers = append(layers, presets.Layers...)
	}
	layers = append(layers, api.PromptSpecLayer(source, frontmatter))
	if err := api.ValidateSpecLayers(layers...); err != nil {
		return nil, fmt.Errorf("prompt configuration: %w", err)
	}
	if user == nil {
		return layers, nil
	}
	request := *user
	if err := api.ValidateSpecLayers(api.RequestSpecLayer(renderRequestLayer, request)); err != nil {
		return nil, err
	}
	return append(layers, api.RequestSpecLayer(renderRequestLayer, request)), nil
}

// renderLayers retains declarations until every request override is available.
// A literal body is not parsed: it carries no frontmatter, so it can pin no
// presets, and reading its opening "---" as YAML would let piped data
// silently redirect the run.
func renderLayers(ctx context.Context, source, content string, frontmatter ai.Request, renderReq PromptRenderRequest, saved captainconfig.Config) ([]api.SpecLayer, []string, error) {
	var pinnedPresets []string
	var pinnedPresetsSet bool
	var deprecatedPin string
	if !renderReq.Literal {
		doc, err := promptlib.Parse(content)
		if err != nil {
			return nil, nil, err
		}
		pinnedPresets = doc.Presets
		pinnedPresetsSet = doc.PresetsSet
		deprecatedPin = doc.RuntimeProfile
	}
	presets, warnings, err := selectRuntimePresets(ctx, runtimePresetSelection{
		Requested: renderReq.Presets, RequestedSet: renderReq.Presets != nil,
		Pin: pinnedPresets, PinSet: pinnedPresetsSet,
		DeprecatedRequest: renderReq.RuntimeProfile, DeprecatedPin: deprecatedPin,
		Config: &saved,
	})
	if err != nil {
		return nil, nil, err
	}
	layers, err := promptLayers(presets, source, frontmatter, renderReq.Spec)
	return layers, warnings, err
}

// configFromResolved projects the runtime knobs providers read off ai.Config.
func configFromResolved(spec api.Spec) ai.Config {
	return ai.Config{Model: spec.Model, Budget: spec.Budget, NoCache: spec.NoCache, SessionID: spec.SessionID}
}

// foldSkillPolicies mirrors enabled permission skills into Memory.Skills, the
// only field the claude runtimes read for skill directories.
func foldSkillPolicies(spec *api.Spec) {
	skills := append([]string(nil), spec.Memory.Skills...)
	skills = append(skills, spec.Permissions.Skills.Enabled()...)
	if len(skills) > 0 {
		spec.Memory.Skills = dedupeStrings(skills)
	}
}
