package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/commons-db/shell"
)

const captainChatSystemPrompt = "You are Captain's coding-agent launcher assistant. Use Captain and Clicky tools when useful, " +
	"prefer read-only inspection unless the user explicitly asks for edits, and keep follow-up guidance concise."

// captainChatProfileProvider serves the chat runtime profile: the "captain
// serve" base layer, then the presets and spec of the profile the request
// names, else the ~/.captain.yaml chat.runtimeProfile default, else nothing.
func captainChatProfileProvider(cwd string) aichat.RuntimeProfileProvider {
	base := api.SpecLayer{
		Name: "captain serve", Scope: api.SpecLayerGlobal,
		Spec: api.Spec{
			Setup: &shell.Setup{Cwd: cwd},
		},
	}
	return aichat.RuntimeProfileProviderFunc(func(ctx context.Context, options ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
		cfg, _, err := captainconfig.Load()
		if err != nil {
			return aichat.RuntimeProfile{}, fmt.Errorf("load chat settings: %w", err)
		}
		if err := cfg.AI.Validate(); err != nil {
			return aichat.RuntimeProfile{}, fmt.Errorf("chat saved defaults: %w", err)
		}
		selection := aichat.ApplyRuntimeProfileOptions(options...)
		layers, err := chatProfileLayers(ctx, chatProfileLayerOptions{Base: base, Selection: selection, Config: cfg, Cwd: cwd})
		if err != nil {
			return aichat.RuntimeProfile{}, err
		}
		composed, err := api.ComposeSpecLayers(api.ResolveSpecOptions{Layers: layers.Layers, Saved: &cfg.AI})
		if err != nil {
			return aichat.RuntimeProfile{}, fmt.Errorf("resolve chat runtime profile: %w", err)
		}
		composed.Warnings = append(composed.Warnings, layers.Warnings...)
		return aichat.RuntimeProfile{System: captainChatSystemPrompt, Composed: composed, Saved: &cfg.AI}, nil
	})
}

type chatProfileLayerOptions struct {
	Base      api.SpecLayer
	Selection aichat.RuntimeProfileOptions
	Config    captainconfig.Config
	Cwd       string
}

// chatProfileLayers appends selected preset layers to the application base.
// Deprecated profile references warn and do not affect the stack.
func chatProfileLayers(ctx context.Context, options chatProfileLayerOptions) (runtimeprofiles.LayerResult, error) {
	if err := api.ValidateSpecLayers(options.Base); err != nil {
		return runtimeprofiles.LayerResult{}, fmt.Errorf("chat runtime preset base: %w", err)
	}
	resolver := runtimeprofiles.NewResolver(func(ctx context.Context) (*runtimeprofiles.Catalog, error) {
		return buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{Config: &options.Config, Cwd: options.Cwd})
	})
	result, err := resolver.Layers(ctx, runtimeprofiles.ResolveOptions{
		BaseLayers:       []api.SpecLayer{options.Base},
		RequestedPresets: options.Selection.Presets, RequestedPresetsSet: options.Selection.PresetsSet,
		DefaultPresets:   options.Config.Chat.Presets,
		RequestedProfile: options.Selection.Ref, DefaultProfile: options.Config.Chat.RuntimeProfile,
	})
	if err != nil {
		var selection *runtimeprofiles.SelectionError
		var owned *runtimeprofiles.OwnedLayersError
		if !errors.As(err, &owned) && errors.As(err, &selection) && selection.Origin == runtimeprofiles.SelectionRequested &&
			(errors.Is(err, runtimeprofiles.ErrNotFound) || errors.Is(err, runtimeprofiles.ErrAmbiguous) || errors.Is(err, runtimeprofiles.ErrCatalogUnavailable)) {
			return runtimeprofiles.LayerResult{}, aichat.RequestError(http.StatusBadRequest, selection.Error())
		}
		return runtimeprofiles.LayerResult{}, err
	}
	return result, nil
}
