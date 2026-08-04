// Receiver repositories: the supervisor-side mailbox and the agent-side
// sidecar bare repo. Both carry the mandated config (R2.2); the mailbox
// additionally shares the real repository's object store via alternates so
// protocol refs never pollute the user's working repo (R2.1/H8).
package gitagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// ReceiverRole distinguishes the two admission tiers.
type ReceiverRole string

const (
	RoleMailbox ReceiverRole = "mailbox"
	RoleSidecar ReceiverRole = "sidecar"
)

// DefaultMaxInputSize caps a single incoming pack (R2.2/H10).
const DefaultMaxInputSize = int64(512 << 20)

// receiverConfig is the R2.2 block every receiving repo MUST carry.
func receiverConfig(maxInputSize int64) [][2]string {
	return [][2]string{
		{"receive.advertisePushOptions", "true"},
		{"receive.fsckObjects", "true"},
		{"receive.denyDeletes", "true"},
		{"receive.autogc", "false"},
		{"core.logAllRefUpdates", "always"},
		{"receive.maxInputSize", strconv.FormatInt(maxInputSize, 10)},
	}
}

// InitMailbox creates (or re-configures — every step is idempotent) the bare
// mailbox repo at path, sharing objects with the real repository at realRepo
// via objects/info/alternates.
func InitMailbox(ctx context.Context, path, realRepo string) error {
	if err := initReceiver(ctx, path); err != nil {
		return err
	}
	realGitDir, err := runGit(ctx, realRepo, ScrubGitEnv(os.Environ()), "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("mailbox: resolving the real repository: %w", err)
	}
	objects := filepath.Join(realGitDir, "objects")
	if _, err := os.Stat(objects); err != nil {
		return fmt.Errorf("mailbox: real repository object store %s: %w", objects, err)
	}
	alternates := filepath.Join(path, "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternates), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(alternates, []byte(objects+"\n"), 0o644)
}

// InitSidecar creates the bare sidecar repo at path.
func InitSidecar(ctx context.Context, path string) error {
	return initReceiver(ctx, path)
}

func initReceiver(ctx context.Context, path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	env := ScrubGitEnv(os.Environ())
	if _, err := runGit(ctx, path, env, "init", "--quiet", "--bare"); err != nil {
		return err
	}
	for _, kv := range receiverConfig(DefaultMaxInputSize) {
		if _, err := runGit(ctx, path, env, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(path, "captain"), 0o755)
}

// writeFileAtomic writes via a sibling temp file and rename, so a reader
// never observes a partial file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
