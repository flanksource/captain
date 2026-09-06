package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
		composed, err := api.ComposeSpecLayers(api.ResolveSpecOptions{Layers: layers, Saved: &cfg.AI})
		if err != nil {
			return aichat.RuntimeProfile{}, fmt.Errorf("resolve chat runtime profile: %w", err)
		}
		return aichat.RuntimeProfile{System: captainChatSystemPrompt, Composed: composed, Saved: &cfg.AI}, nil
	})
}

type chatProfileLayerOptions struct {
	Base      api.SpecLayer
	Selection aichat.RuntimeProfileOptions
	Config    captainconfig.Config
	Cwd       string
}

// chatProfileLayers appends the selected profile's raw layers to the base. A
// reference the caller supplied that resolves nowhere is the caller's error; a
// configured default that fails stays a server error.
func chatProfileLayers(ctx context.Context, options chatProfileLayerOptions) ([]api.SpecLayer, error) {
	if err := api.ValidateSpecLayers(options.Base); err != nil {
		return nil, fmt.Errorf("chat runtime profile base: %w", err)
	}
	ref := strings.TrimSpace(options.Selection.Ref)
	requested := ref != ""
	if !requested {
		ref = strings.TrimSpace(options.Config.Chat.RuntimeProfile)
	}
	if ref == "" {
		return []api.SpecLayer{options.Base}, nil
	}
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{Config: &options.Config, Cwd: options.Cwd})
	if err != nil {
		return nil, fmt.Errorf("chat runtime profile %q: %w", ref, err)
	}
	resolution, err := catalog.Layers(ctx, ref)
	if err != nil {
		var owned *runtimeprofiles.OwnedLayersError
		if requested && !errors.As(err, &owned) && (errors.Is(err, runtimeprofiles.ErrNotFound) || errors.Is(err, runtimeprofiles.ErrAmbiguous)) {
			return nil, aichat.RequestError(http.StatusBadRequest, fmt.Sprintf("runtime profile %q: %v", ref, err))
		}
		return nil, fmt.Errorf("chat runtime profile %q: %w", ref, err)
	}
	return append([]api.SpecLayer{options.Base}, resolution.Layers...), nil
}
