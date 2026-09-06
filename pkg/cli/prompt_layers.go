package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
)

const renderRequestLayer = "render request"

// selectRuntimeProfile picks the profile a render runs under: the caller's
// reference, else the prompt's frontmatter pin, else none. The catalog is only
// built once a reference exists, so a plain render never opens the database;
// a reference that resolves nowhere fails naming it.
type runtimeProfileSelection struct {
	Requested string
	Pin       string
	Config    *captainconfig.Config
}

func selectRuntimeProfile(ctx context.Context, options runtimeProfileSelection) (*runtimeprofiles.Resolution, error) {
	ref := strings.TrimSpace(options.Requested)
	if ref == "" {
		ref = strings.TrimSpace(options.Pin)
	}
	if ref == "" {
		return nil, nil
	}
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{Config: options.Config})
	if err != nil {
		return nil, fmt.Errorf("runtime profile %q: %w", ref, err)
	}
	resolution, err := catalog.Layers(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("runtime profile %q: %w", ref, err)
	}
	return &resolution, nil
}

// promptLayers assembles authored profile, prompt and request layers. Captain's
// final resolution expands the effective model selector while retaining the raw trace.
func promptLayers(profile *runtimeprofiles.Resolution, source string, frontmatter ai.Request, user *api.Spec) ([]api.SpecLayer, error) {
	var layers []api.SpecLayer
	if profile != nil {
		layers = append(layers, profile.Layers...)
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
func renderLayers(ctx context.Context, source, content string, frontmatter ai.Request, renderReq PromptRenderRequest, saved captainconfig.Config) ([]api.SpecLayer, error) {
	doc, err := promptlib.Parse(content)
	if err != nil {
		return nil, err
	}
	profile, err := selectRuntimeProfile(ctx, runtimeProfileSelection{Requested: renderReq.RuntimeProfile, Pin: doc.RuntimeProfile, Config: &saved})
	if err != nil {
		return nil, err
	}
	return promptLayers(profile, source, frontmatter, renderReq.Spec)
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
