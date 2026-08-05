package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	// defaultDatabaseContextName is the reserved name of the database captain
	// monitors, migrates, and writes to. Every other context is read-only.
	defaultDatabaseContextName = "default"
	// databaseContextEnv selects the active context for a shell.
	databaseContextEnv = "CAPTAIN_DB_CONTEXT"
	// databaseContextsEnv defines ad-hoc contexts as ";"- or newline-separated
	// name=dsn entries.
	databaseContextsEnv = "CAPTAIN_DB_CONTEXTS"
)

var errUnknownDatabaseContext = errors.New("unknown database context")

// databaseContextNamePattern keeps context names usable as cookie values, flag
// values, and map keys without quoting.
var databaseContextNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// DatabaseContext is one readable Captain database. Exactly one context is
// active per CLI invocation or HTTP request; only the default is monitored,
// migrated, and written to.
type DatabaseContext struct {
	Name     string
	Label    string
	DSN      string
	Source   string
	Default  bool
	ReadOnly bool
}

type databaseContextKey struct{}

// ContextWithDatabaseContext binds a database context name to ctx. The HTTP
// middleware and the root command's persistent hook are the only writers.
func ContextWithDatabaseContext(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, databaseContextKey{}, name)
}

func databaseContextNameFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	name, ok := ctx.Value(databaseContextKey{}).(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", false
	}
	return name, true
}

// activeDatabaseContextName resolves the context a read should target: the ctx
// value set by the HTTP middleware or the root persistent hook, then the
// --context flag (also the /api/v1 executor's flags path), then the
// environment, then the default.
func activeDatabaseContextName(ctx context.Context) string {
	if name, ok := databaseContextNameFromContext(ctx); ok {
		return name
	}
	if name := strings.TrimSpace(databaseContextFlagValue); name != "" {
		return name
	}
	if name := strings.TrimSpace(os.Getenv(databaseContextEnv)); name != "" {
		return name
	}
	return defaultDatabaseContextName
}

// ResolveDatabaseContextName resolves the active context name and verifies it
// is configured, so an unknown --context is rejected before any command runs
// rather than at the first query.
func ResolveDatabaseContextName(ctx context.Context) (string, error) {
	name := activeDatabaseContextName(ctx)
	if _, err := lookupDatabaseContext(name); err != nil {
		return "", err
	}
	return name, nil
}

// databaseContextCache memoizes config resolution so the HTTP middleware can
// look up a context on every request without re-reading db.json.
var databaseContextCache struct {
	mu       sync.Mutex
	resolved bool
	contexts []DatabaseContext
	err      error
}

// databaseContexts returns every configured context, the default first and the
// rest sorted by name. It opens no connections: the default context's DSN is
// resolved lazily by the registry, because resolving it can start captain's
// embedded postgres.
func databaseContexts() ([]DatabaseContext, error) {
	databaseContextCache.mu.Lock()
	defer databaseContextCache.mu.Unlock()
	if !databaseContextCache.resolved {
		databaseContextCache.contexts, databaseContextCache.err = resolveDatabaseContexts()
		databaseContextCache.resolved = true
	}
	return databaseContextCache.contexts, databaseContextCache.err
}

func lookupDatabaseContext(name string) (DatabaseContext, error) {
	contexts, err := databaseContexts()
	if err != nil {
		return DatabaseContext{}, err
	}
	for _, ctx := range contexts {
		if ctx.Name == name {
			return ctx, nil
		}
	}
	return DatabaseContext{}, fmt.Errorf("%w %q (configured: %s)", errUnknownDatabaseContext, name,
		strings.Join(databaseContextNames(contexts), ", "))
}

func databaseContextNames(contexts []DatabaseContext) []string {
	names := make([]string, 0, len(contexts))
	for _, ctx := range contexts {
		names = append(names, ctx.Name)
	}
	return names
}

// resetDatabaseContextCache drops memoized config so tests (and a future
// config-reload command) observe a changed environment.
func resetDatabaseContextCache() {
	databaseContextCache.mu.Lock()
	databaseContextCache.resolved = false
	databaseContextCache.contexts = nil
	databaseContextCache.err = nil
	databaseContextCache.mu.Unlock()
}

// databaseContextSpec is one context's definition before merging. The default
// context has no spec: it is resolved by captainDSN.
type databaseContextSpec struct {
	Name     string
	DSN      string
	Label    string
	Source   string
	ReadOnly *bool // nil => read-only
}

func resolveDatabaseContexts() ([]DatabaseContext, error) {
	specs, err := configContextSpecs()
	if err != nil {
		return nil, err
	}
	envSpecs, err := envContextSpecs()
	if err != nil {
		return nil, err
	}
	flagSpecs, err := flagContextSpecs()
	if err != nil {
		return nil, err
	}
	// Precedence: --db-url over CAPTAIN_DB_CONTEXTS over db.json.
	for _, overlay := range []map[string]databaseContextSpec{envSpecs, flagSpecs} {
		for name, spec := range overlay {
			specs[name] = spec
		}
	}

	// The default context's DSN and source are filled in once it is opened;
	// resolving them here could start captain's embedded postgres.
	contexts := []DatabaseContext{{
		Name: defaultDatabaseContextName, Label: "Monitored database", Default: true,
	}}
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := specs[name]
		label := spec.Label
		if label == "" {
			label = name
		}
		contexts = append(contexts, DatabaseContext{
			Name:     name,
			Label:    label,
			DSN:      spec.DSN,
			Source:   spec.Source,
			ReadOnly: spec.ReadOnly == nil || *spec.ReadOnly,
		})
	}
	return contexts, nil
}

func flagContextSpecs() (map[string]databaseContextSpec, error) {
	specs := map[string]databaseContextSpec{}
	for _, value := range databaseURLs {
		name, dsn, named := splitContextSpec(value)
		if !named {
			continue
		}
		if err := validateContextSpec(name, dsn, "--"+databaseURLFlag); err != nil {
			return nil, err
		}
		specs[name] = databaseContextSpec{Name: name, DSN: dsn, Source: "--" + databaseURLFlag}
	}
	return specs, nil
}

func envContextSpecs() (map[string]databaseContextSpec, error) {
	specs := map[string]databaseContextSpec{}
	raw := strings.TrimSpace(os.Getenv(databaseContextsEnv))
	if raw == "" {
		return specs, nil
	}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, dsn, named := splitContextSpec(entry)
		if !named {
			return nil, fmt.Errorf("%s entry %q must be name=dsn, with a context name matching %s and a URL-form DSN",
				databaseContextsEnv, entry, databaseContextNamePattern)
		}
		if err := validateContextSpec(name, dsn, databaseContextsEnv); err != nil {
			return nil, err
		}
		specs[name] = databaseContextSpec{Name: name, DSN: dsn, Source: databaseContextsEnv}
	}
	return specs, nil
}

// splitContextSpec splits "name=dsn". A value is named only when the text
// before the first "=" is a plausible context name and the remainder is a URL,
// so libpq keyword DSNs ("host=localhost dbname=gavel") stay unnamed.
func splitContextSpec(value string) (name, dsn string, named bool) {
	value = strings.TrimSpace(value)
	prefix, rest, found := strings.Cut(value, "=")
	if !found {
		return "", value, false
	}
	prefix = strings.TrimSpace(prefix)
	rest = strings.TrimSpace(rest)
	if !databaseContextNamePattern.MatchString(prefix) || !strings.Contains(rest, "://") {
		return "", value, false
	}
	return prefix, rest, true
}

func validateContextSpec(name, dsn, source string) error {
	if name == defaultDatabaseContextName {
		return fmt.Errorf("%s: %q is a reserved context name; it always refers to the monitored database", source, defaultDatabaseContextName)
	}
	if !databaseContextNamePattern.MatchString(name) {
		return fmt.Errorf("%s: invalid context name %q, expected %s", source, name, databaseContextNamePattern)
	}
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("%s: context %q has an empty dsn", source, name)
	}
	return nil
}

// defaultDatabaseURLOverride returns the unnamed --db-url value, which
// overrides the default context's DSN.
func defaultDatabaseURLOverride() (string, error) {
	override := ""
	for _, value := range databaseURLs {
		if _, dsn, named := splitContextSpec(value); !named {
			if override != "" {
				return "", fmt.Errorf("--%s may specify at most one unnamed DSN (the default context); use name=dsn for additional contexts", databaseURLFlag)
			}
			override = dsn
		}
	}
	return override, nil
}
