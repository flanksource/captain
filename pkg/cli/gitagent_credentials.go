package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/agentcreds"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// credentialMaterializeInterval is how often the sidecar re-reads the mounted
// credentials. Kubelet refreshes a projected Secret volume on its own sync
// period (tens of seconds), so polling faster buys nothing; polling much slower
// would let a republished credential sit unused while the old one expires.
const credentialMaterializeInterval = 30 * time.Second

// credentialMaterializer copies the credentials the supervisor publishes into
// the paths the agent CLIs actually read.
//
// The mount cannot simply BE those paths. It is read-only and shared, while
// ~/.claude and ~/.codex are directories the CLIs write their own state into —
// settings, history, session transcripts. Copying the one file out of the mount
// leaves the rest of each state directory writable.
//
// Symlinking would follow the supervisor's updates for free, but into a
// read-only volume, so the first time a CLI tried to rewrite its own credential
// it would fail. Copying plus this poll keeps both properties.
type credentialMaterializer struct {
	// source is the mounted directory, deploy.CredentialsMountPath in a workload.
	source string
	// home is where the CLI config directories live.
	home     string
	interval time.Duration
}

func newCredentialMaterializer(home string) *credentialMaterializer {
	return &credentialMaterializer{
		source:   deploy.CredentialsMountPath,
		home:     home,
		interval: credentialMaterializeInterval,
	}
}

// mounted reports whether a credential volume is present. Absence is the normal
// case for a sidecar deployed without credentials, so it is not an error.
func (m *credentialMaterializer) mounted() bool {
	info, err := os.Stat(m.source)
	return err == nil && info.IsDir()
}

// run materializes now and then keeps the copies in step with the mount.
func (m *credentialMaterializer) run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		if err := m.materialize(); err != nil {
			log.Warnf("git-agent credential materializer: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// credentialTargets maps a published key onto the path its CLI reads.
func (m *credentialMaterializer) credentialTargets() map[string]string {
	return map[string]string{
		agentcreds.ClaudeFilename: filepath.Join(m.home, ".claude", agentcreds.ClaudeRelPath),
		agentcreds.CodexFilename:  filepath.Join(m.home, ".codex", agentcreds.CodexRelPath),
	}
}

// materialize copies each published credential to its CLI path, skipping files
// whose contents already match so the CLIs are not handed a changed mtime on
// every tick.
//
// A mount that exists but cannot be read is reported rather than skipped: the
// workload was deployed with credentials and is not getting them, which is the
// one failure mode this whole path exists to avoid being silent.
func (m *credentialMaterializer) materialize() error {
	for key, target := range m.credentialTargets() {
		source := filepath.Join(m.source, key)
		payload, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			// The supervisor publishes only the providers it is configured for.
			continue
		}
		if err != nil {
			return fmt.Errorf("read published credential %s: %w", source, err)
		}
		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, payload) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create credential directory for %s: %w", target, err)
		}
		if err := writeCredentialFile(target, payload); err != nil {
			return err
		}
		log.Infof("Materialized %s credential to %s", key, target)
	}
	return nil
}

// writeCredentialFile replaces target atomically, so a CLI reading concurrently
// sees either the previous credential or the new one.
func writeCredentialFile(target string, payload []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(target), ".credential-*")
	if err != nil {
		return fmt.Errorf("create temp file beside %s: %w", target, err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure temp file for %s: %w", target, err)
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", target, err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}
