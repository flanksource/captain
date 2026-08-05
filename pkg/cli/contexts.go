package cli

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/database"
)

// ContextsOptions configures the database context listing.
type ContextsOptions struct {
	// Check opens each configured context and reports whether it answers a
	// trivial query. Without it nothing is connected to.
	Check bool `json:"check" flag:"check" description:"Connect to each context and report whether it is reachable"`
}

// RunContexts lists the databases captain can read. Only the default context is
// monitored and written to; every other context is read-only.
func RunContexts(ctx context.Context, opts ContextsOptions) (ContextsResult, error) {
	result, err := describeDatabaseContexts(activeDatabaseContextName(ctx))
	if err != nil {
		return ContextsResult{}, err
	}
	if !opts.Check {
		return result, nil
	}
	for i, row := range result.Contexts {
		result.Contexts[i].Status = checkDatabaseContext(ctx, row.Name)
		if dsn, source := contextDatabaseIdentity(row.Name); source != "" {
			result.Contexts[i].Source = source
			result.Contexts[i].DSN = database.MaskDSN(dsn)
		}
	}
	return result, nil
}

func checkDatabaseContext(ctx context.Context, name string) string {
	db, err := openContextDB(ctx, name, captainDatabaseNoMigrations)
	if err != nil {
		return fmt.Sprintf("unreachable: %v", err)
	}
	if err := db.Gorm().WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		return fmt.Sprintf("unreachable: %v", err)
	}
	return "ok"
}
