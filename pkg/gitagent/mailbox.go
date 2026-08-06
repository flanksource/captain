// Repository-scoped supervisor mailboxes. One receive endpoint hosts many
// bare repositories, while each mailbox has one immutable local integration
// target and one opaque route safe to send to a sidecar.
package gitagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// MailboxesDir is the repository namespace served by a supervisor endpoint.
	MailboxesDir = "mailboxes"

	mailboxBindingVersion = 1
	mailboxBindingFile    = "repository.json"
)

var mailboxRouteRe = regexp.MustCompile(`^mailboxes/[0-9a-f]{64}\.git$`)

// Mailbox identifies the repository-specific receiver used by one supervisor
// worktree. Route is safe to send over the wire; Repository remains local.
type Mailbox struct {
	Path       string
	Route      string
	Repository string
}

// MailboxBinding is the durable, supervisor-local association between a bare
// mailbox and the worktree into which accepted results are integrated.
type MailboxBinding struct {
	Version    int    `json:"version"`
	Repository string `json:"repository"`
}

// ValidateMailboxRoute limits a relayed repository path to Captain's opaque
// mailbox namespace. The route is still containment-checked by the SSH server.
func ValidateMailboxRoute(route string) error {
	if !mailboxRouteRe.MatchString(route) {
		return fmt.Errorf("mailbox route %q must match %s", route, mailboxRouteRe)
	}
	return nil
}

// MailboxForRepository derives a stable receiver path from the canonical
// worktree root. Repositories with the same basename remain distinct.
func MailboxForRepository(ctx context.Context, servedRoot, repoDir string) (Mailbox, error) {
	repository, err := canonicalRepository(ctx, repoDir)
	if err != nil {
		return Mailbox{}, err
	}
	root, err := filepath.Abs(servedRoot)
	if err != nil {
		return Mailbox{}, err
	}
	sum := sha256.Sum256([]byte(repository))
	route := path.Join(MailboxesDir, hex.EncodeToString(sum[:])+".git")
	return Mailbox{
		Path:       filepath.Join(root, filepath.FromSlash(route)),
		Route:      route,
		Repository: repository,
	}, nil
}

// EnsureMailbox creates or verifies the repository-specific mailbox. A
// mailbox binding is immutable: changing it would leave existing refs backed
// by a different object store and make unrelated future pushes fail.
func EnsureMailbox(ctx context.Context, servedRoot, repoDir string) (Mailbox, error) {
	mailbox, err := MailboxForRepository(ctx, servedRoot, repoDir)
	if err != nil {
		return Mailbox{}, err
	}
	if err := os.MkdirAll(filepath.Dir(mailbox.Path), 0o755); err != nil {
		return Mailbox{}, err
	}
	if err := InitMailbox(ctx, mailbox.Path, mailbox.Repository); err != nil {
		return Mailbox{}, err
	}
	return mailbox, nil
}

// LoadMailboxBinding reads the integration target owned by a mailbox hook.
func LoadMailboxBinding(mailboxPath string) (MailboxBinding, error) {
	data, err := os.ReadFile(filepath.Join(mailboxPath, "captain", mailboxBindingFile))
	if err != nil {
		return MailboxBinding{}, fmt.Errorf("mailbox binding: %w", err)
	}
	var binding MailboxBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		return MailboxBinding{}, fmt.Errorf("mailbox binding: %w", err)
	}
	if binding.Version != mailboxBindingVersion || !filepath.IsAbs(binding.Repository) {
		return MailboxBinding{}, fmt.Errorf("mailbox binding has version %d and repository %q", binding.Version, binding.Repository)
	}
	return binding, nil
}

// bindMailbox commits the repository association and alternates path under one
// lock. A partial first initialization can be completed, never redirected.
func bindMailbox(mailboxPath, repository, objects string) error {
	return withFileLock(filepath.Join(mailboxPath, "captain", "mailbox.lock"), 0o600, func() error {
		bindingPath := filepath.Join(mailboxPath, "captain", mailboxBindingFile)
		binding, err := LoadMailboxBinding(mailboxPath)
		bindingExists := err == nil
		switch {
		case bindingExists && binding.Repository != repository:
			return fmt.Errorf("mailbox %s is bound to %s; cannot rebind it to %s", mailboxPath, binding.Repository, repository)
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		}

		alternates := filepath.Join(mailboxPath, "objects", "info", "alternates")
		if data, readErr := os.ReadFile(alternates); readErr == nil {
			current := strings.TrimSpace(string(data))
			if current != objects {
				return fmt.Errorf("mailbox %s uses object store %s; cannot rebind it to %s", mailboxPath, current, objects)
			}
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
		if err := os.MkdirAll(filepath.Dir(alternates), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(alternates, []byte(objects+"\n"), 0o644); err != nil {
			return err
		}
		if bindingExists {
			return nil
		}
		data, marshalErr := json.MarshalIndent(MailboxBinding{
			Version: mailboxBindingVersion, Repository: repository,
		}, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		return writeFileAtomic(bindingPath, append(data, '\n'), 0o644)
	})
}

func canonicalRepository(ctx context.Context, repoDir string) (string, error) {
	root, err := runGit(ctx, repoDir, ScrubGitEnv(os.Environ()), "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("mailbox: resolving repository root: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("mailbox: resolving repository path %s: %w", root, err)
	}
	return filepath.Clean(resolved), nil
}

func repositoryObjects(ctx context.Context, repository string) (string, error) {
	gitDir, err := runGit(ctx, repository, ScrubGitEnv(os.Environ()),
		"rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("mailbox: resolving the real repository: %w", err)
	}
	objects := filepath.Join(gitDir, "objects")
	if _, err := os.Stat(objects); err != nil {
		return "", fmt.Errorf("mailbox: real repository object store %s: %w", objects, err)
	}
	return objects, nil
}
