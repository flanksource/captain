package agent

import (
	"fmt"
	"strings"
)

// Scope controls how much hooks act on. ScopeChanged restricts them to the files
// the agent edited; ScopeAll lets each act on the whole tree.
type Scope string

const (
	ScopeChanged Scope = "changed"
	ScopeAll     Scope = "all"
)

// AllScopes lists every scope in canonical order.
func AllScopes() []Scope { return []Scope{ScopeAll, ScopeChanged} }

// Valid reports whether s is one of the supported scopes.
func (s Scope) Valid() bool {
	for _, x := range AllScopes() {
		if s == x {
			return true
		}
	}
	return false
}

// ScopeList renders the supported scopes as a comma-separated string.
func ScopeList() string {
	parts := make([]string, len(AllScopes()))
	for i, s := range AllScopes() {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}

// ParseScope resolves a CLI/flag value into a Scope, defaulting empty to ScopeAll.
func ParseScope(s string) (Scope, error) {
	switch Scope(s) {
	case "", ScopeAll:
		return ScopeAll, nil
	case ScopeChanged:
		return ScopeChanged, nil
	default:
		return "", fmt.Errorf("invalid --scope %q (valid: %s)", s, ScopeList())
	}
}
