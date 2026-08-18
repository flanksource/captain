package captainconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CredentialDefaults is the `credentials:` block of ~/.captain.yaml. It tells
// the supervisor which agent logins to mirror and where to keep them fresh.
//
// This package stays data-only: it validates the shape and expands paths, but
// building publishers and talking to a cluster belongs to pkg/credsync.
type CredentialDefaults struct {
	// RefreshMargin is how far ahead of expiry a credential is republished.
	// Zero means the publisher's own default.
	RefreshMargin time.Duration `yaml:"refreshMargin,omitempty"`
	// Publish is the set of destinations. Empty disables publishing entirely,
	// which is the default: mirroring a credential off this host is opt-in.
	Publish []CredentialPublish `yaml:"publish,omitempty"`
}

// IsZero lets yaml omit an empty credentials block.
func (c CredentialDefaults) IsZero() bool {
	return c.RefreshMargin == 0 && len(c.Publish) == 0
}

// CredentialPublish is one destination and the providers written to it.
type CredentialPublish struct {
	// Providers names the logins to mirror ("claude", "codex"). Empty means all
	// supported providers.
	Providers []string `yaml:"providers,omitempty"`
	// Directory is a path on this host that a Docker workload bind-mounts.
	Directory string `yaml:"directory,omitempty"`
	// Kubernetes publishes into a Secret that a sidecar mounts.
	Kubernetes *CredentialSecretRef `yaml:"kubernetes,omitempty"`
}

// CredentialSecretRef locates the Secret credentials are written to.
type CredentialSecretRef struct {
	// Context names a kubeconfig context; empty uses the current one.
	Context string `yaml:"context,omitempty"`
	// Namespace is required — defaulting it would publish a credential into
	// whichever namespace a kubeconfig happens to point at.
	Namespace string `yaml:"namespace"`
	// Secret defaults to credsync.DefaultSecretName when empty.
	Secret string `yaml:"secret,omitempty"`
}

// Validate refuses a destination that names nowhere to write, or that names
// both kinds at once.
//
// Both are configuration mistakes that would otherwise surface as a supervisor
// that starts, logs nothing, and quietly publishes no credentials.
func (p CredentialPublish) Validate() error {
	hasDirectory := strings.TrimSpace(p.Directory) != ""
	if !hasDirectory && p.Kubernetes == nil {
		return fmt.Errorf("credentials.publish entry names neither a directory nor a kubernetes secret")
	}
	if hasDirectory && p.Kubernetes != nil {
		return fmt.Errorf("credentials.publish entry names both a directory and a kubernetes secret; use one entry per destination")
	}
	if p.Kubernetes != nil && strings.TrimSpace(p.Kubernetes.Namespace) == "" {
		return fmt.Errorf("credentials.publish kubernetes entry requires a namespace")
	}
	return nil
}

// Validate checks every destination.
func (c CredentialDefaults) Validate() error {
	for i, publish := range c.Publish {
		if err := publish.Validate(); err != nil {
			return fmt.Errorf("credentials.publish[%d]: %w", i, err)
		}
	}
	return nil
}

// ResolvedDirectory expands ~ and makes the path absolute, so a configured
// destination means the same thing regardless of the supervisor's cwd.
func (p CredentialPublish) ResolvedDirectory() (string, error) {
	path := strings.TrimSpace(p.Directory)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for credentials.publish directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve credentials.publish directory %q: %w", p.Directory, err)
	}
	return absolute, nil
}
