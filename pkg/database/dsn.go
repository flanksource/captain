package database

import "strings"

// MaskDSN hides the password in URL and key-value PostgreSQL connection
// strings so the selected database can be named safely in startup logs.
func MaskDSN(dsn string) string {
	if strings.Contains(dsn, "://") {
		if at := strings.LastIndex(dsn, "@"); at > 0 {
			if colon := strings.Index(dsn[:at], "://"); colon >= 0 {
				schemeEnd := colon + 3
				credentials := dsn[schemeEnd:at]
				if user, _, ok := strings.Cut(credentials, ":"); ok {
					return dsn[:schemeEnd] + user + ":REDACTED" + dsn[at:]
				}
			}
		}
		return dsn
	}
	parts := strings.Fields(dsn)
	for i, part := range parts {
		if strings.HasPrefix(part, "password=") {
			parts[i] = "password=REDACTED"
		}
	}
	return strings.Join(parts, " ")
}
