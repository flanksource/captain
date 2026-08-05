package cli

import (
	"fmt"
	"net/url"
	"strings"
)

// readOnlySessionOption forces every session on a secondary context's pool into
// read-only transactions, so a read path handed a writing query fails at the
// database rather than mutating somebody else's session store.
const readOnlySessionOption = "-c default_transaction_read_only=on"

// readOnlyDSN returns dsn with the read-only session option merged in. URL-form
// DSNs keep any existing options; keyword-form DSNs that already set options
// are rejected rather than silently mangled — use "readOnly": false in db.json
// for those.
func readOnlyDSN(dsn string) (string, error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return "", fmt.Errorf("cannot make an empty DSN read-only")
	}
	if !strings.Contains(trimmed, "://") {
		if keywordDSNHasOptions(trimmed) {
			return "", fmt.Errorf("cannot add %q to a keyword DSN that already sets options; set \"readOnly\": false for this context", readOnlySessionOption)
		}
		return trimmed + " options='" + readOnlySessionOption + "'", nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse DSN: %w", err)
	}
	query := parsed.Query()
	existing := strings.TrimSpace(query.Get("options"))
	if strings.Contains(existing, "default_transaction_read_only") {
		return trimmed, nil
	}
	if existing == "" {
		query.Set("options", readOnlySessionOption)
	} else {
		query.Set("options", existing+" "+readOnlySessionOption)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func keywordDSNHasOptions(dsn string) bool {
	for _, field := range strings.Fields(dsn) {
		if key, _, found := strings.Cut(field, "="); found && strings.EqualFold(strings.TrimSpace(key), "options") {
			return true
		}
	}
	return false
}
