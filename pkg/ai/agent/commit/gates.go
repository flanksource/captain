package commit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// DefaultMaxFileSize is the cheap gate's per-file ceiling. An agent that writes
// something this large has almost always produced a binary, a build artifact or
// a log rather than source, and a per-turn chain would carry every revision of
// it forever.
const DefaultMaxFileSize = 1 << 20 // 1 MiB

// secretBaseNames are file names that carry credentials often enough that
// committing one unattended is never the intended outcome.
var secretBaseNames = map[string]bool{
	".env":                 true,
	".netrc":               true,
	".npmrc":               true,
	".pypirc":              true,
	"credentials.json":     true,
	"id_rsa":               true,
	"id_ed25519":           true,
	"service-account.json": true,
}

// secretSuffixes are extensions whose contents are keys by definition.
var secretSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".keystore"}

// CheckGates applies the policy's pre-commit checks to the paths about to be
// staged. It rejects rather than filters: dropping a file would produce a commit
// that silently disagrees with the run's diff.
func CheckGates(dir string, level api.CommitGates, maxSize int64, paths []string) error {
	if level != api.CommitGatesCheap {
		return nil
	}
	if maxSize <= 0 {
		maxSize = DefaultMaxFileSize
	}
	var secrets, oversized []string
	for _, p := range paths {
		if LooksSecret(p) {
			secrets = append(secrets, p)
			continue
		}
		info, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			// A staged deletion has no file on disk; that is not a gate failure.
			continue
		}
		if info.Size() > maxSize {
			oversized = append(oversized, fmt.Sprintf("%s (%d bytes)", p, info.Size()))
		}
	}
	if len(secrets) > 0 {
		return fmt.Errorf("commit: refusing to commit files that look like credentials: %s — gitignore them, or set gates: none to commit them deliberately", strings.Join(secrets, ", "))
	}
	if len(oversized) > 0 {
		return fmt.Errorf("commit: refusing to commit files over %d bytes: %s — gitignore them, or set gates: none", maxSize, strings.Join(oversized, ", "))
	}
	return nil
}

// LooksSecret reports whether a repo-relative path names a credential file, by
// base name (including the .env.production family) or by extension.
func LooksSecret(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if secretBaseNames[base] {
		return true
	}
	if strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") {
		return true
	}
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}
