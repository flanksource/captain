package credsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/agentcreds"
)

// DirectoryTarget publishes credentials into a directory on this host, which a
// Docker workload bind-mounts read-only.
//
// It is the docker-side counterpart of KubernetesTarget: the git-agent deploy
// path already does exactly this for the join token, writing it to a host file
// and mounting it at /run/captain/join.
type DirectoryTarget struct {
	Path string
}

func (t DirectoryTarget) Name() string { return "directory " + t.Path }

// Publish writes each credential as its own file, replacing the previous
// contents atomically so a workload reading the directory never observes a
// half-written credential.
func (t DirectoryTarget) Publish(_ context.Context, credentials []agentcreds.Credential) error {
	if err := os.MkdirAll(t.Path, 0o700); err != nil {
		return fmt.Errorf("create credential directory %s: %w", t.Path, err)
	}
	// MkdirAll leaves an existing directory's mode alone, so an inherited
	// world-readable directory is tightened rather than trusted.
	if err := os.Chmod(t.Path, 0o700); err != nil {
		return fmt.Errorf("secure credential directory %s: %w", t.Path, err)
	}
	for _, credential := range credentials {
		if err := writeFileAtomic(filepath.Join(t.Path, credential.Filename), credential.Payload); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic replaces path via a temp file and rename, so a reader sees
// either the old credential or the new one.
func writeFileAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".credsync-*")
	if err != nil {
		return fmt.Errorf("create temp file beside %s: %w", path, err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure temp file for %s: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
