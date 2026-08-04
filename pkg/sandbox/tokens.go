package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
)

// log is the package-scoped logger for sandbox token management. Its level
// follows -v/--log-level and can be tuned with -Plog.level.sandbox=debug.
var log = logger.GetLogger("sandbox")

type AWSTokenConfig struct {
	Profile    string `yaml:"profile,omitempty" json:"profile,omitempty"`
	AssumeRole string `yaml:"assumeRole,omitempty" json:"assumeRole,omitempty"`
	Region     string `yaml:"region,omitempty" json:"region,omitempty"`
}

type GCPTokenConfig struct {
	Project     string `yaml:"project,omitempty" json:"project,omitempty"`
	Credentials string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
}

type AzureTokenConfig struct {
	ClientID     string `yaml:"clientID,omitempty" json:"clientID,omitempty"`
	ClientSecret string `yaml:"clientSecret,omitempty" json:"clientSecret,omitempty"`
	TenantID     string `yaml:"tenantID,omitempty" json:"tenantID,omitempty"`
}

type GitHubTokenConfig struct{}

type K8sTokenConfig struct {
	Context string `yaml:"context,omitempty" json:"context,omitempty"`
}

type TokensConfig struct {
	AWS        *AWSTokenConfig    `yaml:"aws,omitempty" json:"aws,omitempty"`
	GCP        *GCPTokenConfig    `yaml:"gcp,omitempty" json:"gcp,omitempty"`
	Azure      *AzureTokenConfig  `yaml:"azure,omitempty" json:"azure,omitempty"`
	GitHub     *GitHubTokenConfig `yaml:"github,omitempty" json:"github,omitempty"`
	Kubernetes *K8sTokenConfig    `yaml:"kubernetes,omitempty" json:"kubernetes,omitempty"`
}

type TokenResult struct {
	Provider   string
	EnvVars    map[string]string
	WritePaths []string
	Expiry     time.Time
	// Placeholder, when set, is what the sandbox environment carries instead
	// of the real values: an egress proxy (pkg/gitagent/proxy) substitutes it
	// at the granted header on the way out, so the credential never enters
	// the sandbox (issue #39 §6.2, SPEC-git-agent-protocol §9).
	Placeholder string
}

// PlaceholderEnv returns EnvVars with every value replaced by the
// placeholder, for building a sandbox environment that holds no credential.
// It returns nil when no placeholder is set — the caller must not fall back
// to real values by accident.
func (t TokenResult) PlaceholderEnv() map[string]string {
	if t.Placeholder == "" {
		return nil
	}
	env := make(map[string]string, len(t.EnvVars))
	for k := range t.EnvVars {
		env[k] = t.Placeholder
	}
	return env
}

type TokenManager struct {
	credDir string
	stopCh  chan struct{}
	mu      sync.Mutex
	results []TokenResult
}

func NewTokenManager(credDir string) *TokenManager {
	return &TokenManager{
		credDir: credDir,
		stopCh:  make(chan struct{}),
	}
}

func (tm *TokenManager) Acquire(ctx context.Context, config *TokensConfig) ([]TokenResult, error) {
	if config == nil {
		return nil, nil
	}

	var results []TokenResult

	type providerEntry struct {
		name    string
		acquire func() (*TokenResult, error)
	}

	var providers []providerEntry
	if config.GitHub != nil {
		cfg := *config.GitHub
		providers = append(providers, providerEntry{"github", func() (*TokenResult, error) { return acquireGitHubToken(ctx, cfg) }})
	}
	if config.AWS != nil {
		cfg := *config.AWS
		providers = append(providers, providerEntry{"aws", func() (*TokenResult, error) { return acquireAWSToken(ctx, cfg, tm.credDir) }})
	}
	if config.GCP != nil {
		cfg := *config.GCP
		providers = append(providers, providerEntry{"gcp", func() (*TokenResult, error) { return acquireGCPToken(ctx, cfg, tm.credDir) }})
	}
	if config.Azure != nil {
		cfg := *config.Azure
		providers = append(providers, providerEntry{"azure", func() (*TokenResult, error) { return acquireAzureToken(ctx, cfg) }})
	}
	if config.Kubernetes != nil {
		cfg := *config.Kubernetes
		providers = append(providers, providerEntry{"kubernetes", func() (*TokenResult, error) { return acquireK8sToken(ctx, cfg, tm.credDir) }})
	}

	for _, p := range providers {
		log.Infof("Acquiring %s token", p.name)
		r, err := p.acquire()
		if err != nil {
			return nil, fmt.Errorf("%s token: %w", p.name, err)
		}
		for _, path := range r.WritePaths {
			log.Debugf("wrote %s credentials to %s", p.name, path)
		}
		for k := range r.EnvVars {
			log.Debugf("set %s env var for %s", k, p.name)
		}
		if !r.Expiry.IsZero() {
			log.Debugf("%s token expires %s", p.name, r.Expiry.Format("15:04:05"))
		}
		results = append(results, *r)
	}

	tm.mu.Lock()
	tm.results = results
	tm.mu.Unlock()

	return results, nil
}

func (tm *TokenManager) StartRefresh(ctx context.Context, config *TokensConfig, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				tm.mu.Lock()
				for _, r := range tm.results {
					if r.Expiry.IsZero() || time.Until(r.Expiry) > 5*time.Minute {
						continue
					}
					log.Debugf("Refreshing token for provider %s (expires %s)", r.Provider, r.Expiry)
				}
				tm.mu.Unlock()
				if _, err := tm.Acquire(ctx, config); err != nil {
					log.Warnf("Token refresh failed: %v", err)
				}
			case <-tm.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (tm *TokenManager) Cleanup() {
	select {
	case <-tm.stopCh:
	default:
		close(tm.stopCh)
	}
	if tm.credDir != "" {
		os.RemoveAll(tm.credDir)
	}
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func MergeTokensConfig(base, other *TokensConfig) *TokensConfig {
	if base == nil {
		return other
	}
	if other == nil {
		return base
	}
	merged := *base
	if other.AWS != nil {
		merged.AWS = other.AWS
	}
	if other.GCP != nil {
		merged.GCP = other.GCP
	}
	if other.Azure != nil {
		merged.Azure = other.Azure
	}
	if other.GitHub != nil {
		merged.GitHub = other.GitHub
	}
	if other.Kubernetes != nil {
		merged.Kubernetes = other.Kubernetes
	}
	return &merged
}
