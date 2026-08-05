package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/database"
)

const (
	// databaseContextCookie is how the web UI selects a context. A cookie
	// rather than a header because EventSource streams and the embedded chat
	// transport cannot set request headers.
	databaseContextCookie = "captain_db_context"
	// databaseContextHeader lets non-browser clients select a context.
	databaseContextHeader = "X-Captain-DB-Context"
)

// databaseContextError is the machine-readable body the web UI branches on: an
// unknown context clears its stale cookie, a read-only rejection explains why
// the control was refused.
type databaseContextError struct {
	Error    string   `json:"error"`
	Code     string   `json:"code"`
	Contexts []string `json:"contexts,omitempty"`
}

// DatabaseContextMiddleware binds one database context to each request, taken
// from the X-Captain-DB-Context header, else the captain_db_context cookie,
// else the default. An unknown context is rejected rather than silently
// defaulted, and writes are rejected on a read-only context.
func DatabaseContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := requestDatabaseContextName(r)
		dbContext, err := lookupDatabaseContext(name)
		if errors.Is(err, errUnknownDatabaseContext) {
			names := []string{}
			if contexts, listErr := databaseContexts(); listErr == nil {
				names = databaseContextNames(contexts)
			}
			writeDatabaseContextError(w, http.StatusBadRequest, databaseContextError{
				Error: fmt.Sprintf("unknown database context %q", name), Code: "unknown_context", Contexts: names,
			})
			return
		}
		if err != nil {
			writeDatabaseContextError(w, http.StatusInternalServerError, databaseContextError{
				Error: err.Error(), Code: "database_context_config",
			})
			return
		}
		if !dbContext.Default && !isReadOnlyMethod(r.Method) {
			writeDatabaseContextError(w, http.StatusConflict, databaseContextError{
				Error: fmt.Sprintf("database context %q is read-only; switch to the %q context to write", name, defaultDatabaseContextName),
				Code:  "read_only_context",
			})
			return
		}
		next.ServeHTTP(w, r.WithContext(ContextWithDatabaseContext(r.Context(), name)))
	})
}

func requestDatabaseContextName(r *http.Request) string {
	if name := strings.TrimSpace(r.Header.Get(databaseContextHeader)); name != "" {
		return name
	}
	if cookie, err := r.Cookie(databaseContextCookie); err == nil {
		if name := strings.TrimSpace(cookie.Value); name != "" {
			return name
		}
	}
	return defaultDatabaseContextName
}

func isReadOnlyMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func writeDatabaseContextError(w http.ResponseWriter, status int, body databaseContextError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Errorf("write database context error: %v", err)
	}
}

// serveRunStatus maps a Run* failure to a status code. A database context that
// is configured but unreachable is a server-side availability problem, not a
// malformed request.
func serveRunStatus(err error, fallback int) int {
	if errors.Is(err, errUnknownDatabaseContext) || strings.Contains(err.Error(), "open captain database context") {
		return http.StatusServiceUnavailable
	}
	return fallback
}

// ContextsResult lists the database contexts this captain can read.
type ContextsResult struct {
	Active   string       `json:"active" pretty:"label=Active"`
	Default  string       `json:"default" pretty:"label=Default"`
	Contexts []ContextRow `json:"contexts" pretty:"label=Contexts,table"`
}

// ContextRow is one configured database context.
type ContextRow struct {
	Name     string `json:"name" pretty:"label=Name,table"`
	Label    string `json:"label" pretty:"label=Label,table"`
	Source   string `json:"source" pretty:"label=Source,table"`
	DSN      string `json:"dsn" pretty:"label=DSN,table"`
	Default  bool   `json:"default" pretty:"label=Default,table"`
	ReadOnly bool   `json:"readOnly" pretty:"label=Read Only,table"`
	Status   string `json:"status,omitempty" pretty:"label=Status,table"`
}

func handleContexts() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := RunContexts(r.Context(), ContextsOptions{})
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusInternalServerError))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	}
}

// describeDatabaseContexts renders the configured contexts. The default
// context's DSN is reported only once it has been opened, because resolving it
// can start captain's embedded postgres.
func describeDatabaseContexts(active string) (ContextsResult, error) {
	contexts, err := databaseContexts()
	if err != nil {
		return ContextsResult{}, err
	}
	result := ContextsResult{Active: active, Default: defaultDatabaseContextName}
	for _, dbContext := range contexts {
		row := ContextRow{
			Name: dbContext.Name, Label: dbContext.Label, Source: dbContext.Source,
			DSN: database.MaskDSN(dbContext.DSN), Default: dbContext.Default, ReadOnly: dbContext.ReadOnly,
		}
		if openedDSN, openedSource := contextDatabaseIdentity(dbContext.Name); openedSource != "" {
			row.Source, row.DSN = openedSource, database.MaskDSN(openedDSN)
		}
		if row.Label == "" {
			row.Label = row.Source
		}
		if row.Label == "" {
			row.Label = row.Name
		}
		result.Contexts = append(result.Contexts, row)
	}
	return result, nil
}
