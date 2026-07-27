package database

import (
	"os"
	"strings"
)

// LocalHostID returns the canonical identity used by every local Captain
// session producer. Keeping it shared prevents launchers and transcript
// monitors from creating different rows for the same provider thread.
func LocalHostID() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "local"
	}
	return strings.TrimSpace(host)
}
