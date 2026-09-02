package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
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
			Model: api.Model{Name: "sol", Mode: registry.ModeAgent},
			Setup: &shell.Setup{Cwd: cwd},
		},
	}
	return aichat.RuntimeProfileProviderFunc(func(ctx context.Context, options ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
		selection := aichat.ApplyRuntimeProfileOptions(options...)
		layers, err := chatProfileLayers(ctx, base, selection)
		if err != nil {
			return aichat.RuntimeProfile{}, err
		}
		resolved, err := api.ResolveSpecLayers(layers...)
		if err != nil {
			return aichat.RuntimeProfile{}, fmt.Errorf("resolve chat runtime profile: %w", err)
		}
		return aichat.RuntimeProfile{System: captainChatSystemPrompt, Resolved: resolved}, nil
	})
}

// chatProfileLayers appends the selected profile's trace to the base layer. A
// reference the caller supplied that resolves nowhere is the caller's error; a
// configured default that fails stays a server error.
func chatProfileLayers(ctx context.Context, base api.SpecLayer, selection aichat.RuntimeProfileOptions) ([]api.SpecLayer, error) {
	ref := strings.TrimSpace(selection.Ref)
	requested := ref != ""
	if !requested {
		cfg, _, err := captainconfig.Load()
		if err != nil {
			return nil, fmt.Errorf("load chat runtime profile default: %w", err)
		}
		ref = strings.TrimSpace(cfg.Chat.RuntimeProfile)
	}
	if ref == "" {
		return []api.SpecLayer{base}, nil
	}
	catalog, err := buildRuntimeCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("chat runtime profile %q: %w", ref, err)
	}
	resolution, err := catalog.Resolve(ctx, ref)
	if err != nil {
		if requested && (errors.Is(err, runtimeprofiles.ErrNotFound) || errors.Is(err, runtimeprofiles.ErrAmbiguous)) {
			return nil, aichat.RequestError(http.StatusBadRequest, fmt.Sprintf("runtime profile %q: %v", ref, err))
		}
		return nil, fmt.Errorf("chat runtime profile %q: %w", ref, err)
	}
	return append([]api.SpecLayer{base}, resolution.Resolved.Trace...), nil
}
