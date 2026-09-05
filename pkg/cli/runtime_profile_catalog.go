package cli

import (
	"context"
	"errors"
	"net/http"

	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/clicky/entity"
)

type runtimeCatalogContextKey struct{}

// ContextWithRuntimeCatalog pins the preset/profile catalog a request resolves
// against, so tests and embedding hosts can supply file-only sources.
func ContextWithRuntimeCatalog(ctx context.Context, catalog *runtimeprofiles.Catalog) context.Context {
	return context.WithValue(ctx, runtimeCatalogContextKey{}, catalog)
}

// buildRuntimeCatalog assembles the preset and profile sources: the monitored
// database, the user's ~/.config/captain directories, the directories named in
// ~/.captain.yaml, and the repository's .captain directories.
func buildRuntimeCatalog(ctx context.Context) (*runtimeprofiles.Catalog, error) {
	if catalog, ok := ctx.Value(runtimeCatalogContextKey{}).(*runtimeprofiles.Catalog); ok && catalog != nil {
		return catalog, nil
	}
	return runtimeprofiles.NewDefaultCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{
		Read: captainDB, Write: captainDefaultDB,
	})
}

// runtimeCatalogError maps catalog failures onto HTTP statuses for the entity
// surface; anything unrecognised stays an internal error.
func runtimeCatalogError(err error) error {
	var referenced runtimeprofiles.ReferencedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &referenced):
		return entity.NewStatusErrorf(http.StatusConflict, "preset_in_use", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrNotFound):
		return entity.NewStatusErrorf(http.StatusNotFound, "not_found", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrAmbiguous):
		return entity.NewStatusErrorf(http.StatusConflict, "ambiguous", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrNameTaken):
		return entity.NewStatusErrorf(http.StatusConflict, "name_taken", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrReadOnly):
		return entity.NewStatusErrorf(http.StatusConflict, "read_only", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrInvalid):
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid", "%v", err)
	default:
		return err
	}
}
