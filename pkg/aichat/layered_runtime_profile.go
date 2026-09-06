package aichat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
)

// RuntimeProfileBase contains application-owned facts below selected profiles.
type RuntimeProfileBase struct {
	System         string
	Layers         []api.SpecLayer
	ProviderConfig api.Config
}

// RuntimeProfileBaseProvider loads application-owned profile facts per request.
type RuntimeProfileBaseProvider func(context.Context) (RuntimeProfileBase, error)

// RuntimeProfileDefaultProvider lazily loads the host's default profile ref.
type RuntimeProfileDefaultProvider func(context.Context) (string, error)

// LayeredRuntimeProfileProviderOptions configures a shared profile provider.
type LayeredRuntimeProfileProviderOptions struct {
	Resolver       *runtimeprofiles.Resolver
	Base           RuntimeProfileBaseProvider
	DefaultProfile RuntimeProfileDefaultProvider
}

// NewLayeredRuntimeProfileProvider resolves host facts and selected profiles.
func NewLayeredRuntimeProfileProvider(options LayeredRuntimeProfileProviderOptions) (RuntimeProfileProvider, error) {
	if options.Resolver == nil {
		return nil, fmt.Errorf("layered runtime profile resolver is required")
	}
	if options.Base == nil {
		return nil, fmt.Errorf("layered runtime profile base provider is required")
	}
	return RuntimeProfileProviderFunc(func(ctx context.Context, requestOptions ...RuntimeProfileOption) (RuntimeProfile, error) {
		request := ApplyRuntimeProfileOptions(requestOptions...)
		base, err := options.Base(ctx)
		if err != nil {
			return RuntimeProfile{}, fmt.Errorf("load runtime profile base: %w", err)
		}
		if err := api.ValidateSpecLayers(base.Layers...); err != nil {
			return RuntimeProfile{}, fmt.Errorf("chat runtime profile base: %w", err)
		}
		var defaultProfile string
		if request.Ref == "" && options.DefaultProfile != nil {
			defaultProfile, err = options.DefaultProfile(ctx)
			if err != nil {
				return RuntimeProfile{}, fmt.Errorf("load default runtime profile: %w", err)
			}
		}
		result, err := options.Resolver.Layers(ctx, runtimeprofiles.ResolveOptions{
			BaseLayers: base.Layers, RequestedProfile: request.Ref, DefaultProfile: strings.TrimSpace(defaultProfile),
		})
		if err != nil {
			var selection *runtimeprofiles.SelectionError
			var owned *runtimeprofiles.OwnedLayersError
			if !errors.As(err, &owned) && errors.As(err, &selection) && selection.Origin == runtimeprofiles.SelectionRequested &&
				(errors.Is(err, runtimeprofiles.ErrNotFound) ||
					errors.Is(err, runtimeprofiles.ErrAmbiguous) ||
					errors.Is(err, runtimeprofiles.ErrCatalogUnavailable)) {
				return RuntimeProfile{}, RequestError(http.StatusBadRequest, selection.Error())
			}
			return RuntimeProfile{}, err
		}
		composed, err := api.ComposeSpecLayers(result.Layers...)
		if err != nil {
			return RuntimeProfile{}, fmt.Errorf("compose chat runtime profile: %w", err)
		}
		return RuntimeProfile{
			System: base.System, Composed: composed, ProviderConfig: base.ProviderConfig,
		}, nil
	}), nil
}
