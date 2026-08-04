package gitagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const hookShimMarker = "# installed by captain sandbox git-agent"

// InstallHookShims writes pre-receive and post-receive shims into the repo's
// hooks directory, each exec'ing the captain binary's hook subcommand with
// stdin and environment flowing through untouched. Re-running is idempotent:
// an identical shim is left alone, a stale one is rewritten, and a foreign
// hook is refused rather than silently replaced.
func InstallHookShims(repoPath, captainBin string, role ReceiverRole) error {
	bin, err := filepath.Abs(captainBin)
	if err != nil {
		return err
	}
	repo, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	for _, hook := range []string{"pre-receive", "post-receive"} {
		shim := fmt.Sprintf(`#!/bin/sh
%s
exec %q sandbox git-agent hook %s --repo %q --role %q
`, hookShimMarker, bin, hook, repo, string(role))
		target := filepath.Join(repo, "hooks", hook)
		existing, err := os.ReadFile(target)
		switch {
		case err == nil && string(existing) == shim:
			continue
		case err == nil && !containsMarker(existing):
			return fmt.Errorf("%s already has a %s hook not installed by captain; remove it first", repo, hook)
		case err != nil && !os.IsNotExist(err):
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(target, []byte(shim), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func containsMarker(content []byte) bool {
	return strings.Contains(string(content), hookShimMarker)
}
