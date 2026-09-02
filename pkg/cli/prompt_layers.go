package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
)

const renderRequestLayer = "render request"

// selectRuntimeProfile picks the profile a render runs under: the caller's
// reference, else the prompt's frontmatter pin, else none. The catalog is only
// built once a reference exists, so a plain render never opens the database;
// a reference that resolves nowhere fails naming it.
func selectRuntimeProfile(ctx context.Context, requested, pin string) (*runtimeprofiles.Resolution, error) {
	ref := strings.TrimSpace(requested)
	if ref == "" {
		ref = strings.TrimSpace(pin)
	}
	if ref == "" {
		return nil, nil
	}
	catalog, err := buildRuntimeCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime profile %q: %w", ref, err)
	}
	resolution, err := catalog.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("runtime profile %q: %w", ref, err)
	}
	return &resolution, nil
}

// promptLayers orders a render's layers: the profile's presets and spec, the
// prompt frontmatter, then the caller's request. The request model is expanded
// first (the aiflags invariant) so a mode-prefixed selector such as api:opus
// overrides the frontmatter mode while a bare name inherits it.
func promptLayers(profile *runtimeprofiles.Resolution, source string, frontmatter ai.Request, user *api.Spec) ([]api.SpecLayer, error) {
	var layers []api.SpecLayer
	if profile != nil {
		layers = append(layers, profile.Resolved.Trace...)
	}
	layers = append(layers, api.PromptSpecLayer(source, frontmatter))
	if user == nil {
		return layers, nil
	}
	request := *user
	model, err := request.Expand()
	if err != nil {
		return nil, fmt.Errorf("render request model: %w", err)
	}
	request.Model = model
	return append(layers, api.RequestSpecLayer(renderRequestLayer, request)), nil
}

func resolvePromptLayers(layers []api.SpecLayer) (api.ResolvedSpec, error) {
	resolved, err := api.ResolveSpecLayers(layers...)
	if err != nil {
		return api.ResolvedSpec{}, fmt.Errorf("resolve prompt spec layers: %w", err)
	}
	return resolved, nil
}

// resolveRenderLayers is the shared seam every render path goes through:
// select the profile (request reference, else the frontmatter pin), then
// resolve presets → profile → frontmatter → request.
func resolveRenderLayers(ctx context.Context, source, content string, frontmatter ai.Request, renderReq PromptRenderRequest) (api.ResolvedSpec, error) {
	doc, err := promptlib.Parse(content)
	if err != nil {
		return api.ResolvedSpec{}, err
	}
	profile, err := selectRuntimeProfile(ctx, renderReq.RuntimeProfile, doc.RuntimeProfile)
	if err != nil {
		return api.ResolvedSpec{}, err
	}
	layers, err := promptLayers(profile, source, frontmatter, renderReq.Spec)
	if err != nil {
		return api.ResolvedSpec{}, err
	}
	return resolvePromptLayers(layers)
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
