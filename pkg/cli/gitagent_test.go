package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
)

func isolatedConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".captain.yaml")
	captainconfig.SetPathForTesting(path)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
	return path
}

func TestGitAgentAddMintsSingleUseToken(t *testing.T) {
	path := isolatedConfig(t)

	res, err := RunGitAgentAdd(GitAgentAddOptions{Name: "worker-1", Backend: "git-agent"})
	if err != nil {
		t.Fatal(err)
	}
	add, ok := res.(GitAgentAddResult)
	if !ok {
		t.Fatalf("result = %T", res)
	}
	if add.HostFingerprint == "" || !strings.Contains(add.JoinCommand, "--join ") {
		t.Fatalf("join hand-off incomplete: %+v", add)
	}
	if !strings.Contains(add.JoinCommand, "--host-fingerprint "+add.HostFingerprint) {
		t.Fatalf("join command must pin the host key: %s", add.JoinCommand)
	}
	if time.Until(add.Expires) > gitagent.JoinTokenTTL {
		t.Fatalf("token TTL too long: %s", add.Expires)
	}

	// The raw token never lands in the config file — only its hash (R8.2).
	token := strings.Fields(strings.SplitAfter(add.JoinCommand, "--join ")[1])[0]
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("the raw join token must not be persisted")
	}
	if !strings.Contains(string(raw), gitagent.HashJoinToken(token)) {
		t.Fatal("the token hash must be persisted as pending")
	}

	// Consume: valid once, burned after (R8.2).
	dir := gitAgentDirectory{backend: "git-agent"}
	name, err := dir.ConsumeJoinToken(token)
	if err != nil || name != "worker-1" {
		t.Fatalf("consume = %q, %v", name, err)
	}
	if _, err := dir.ConsumeJoinToken(token); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("replay must fail, got %v", err)
	}
}

func TestGitAgentExpiredTokenRefused(t *testing.T) {
	isolatedConfig(t)
	token, hash, err := gitagent.MintJoinToken()
	if err != nil {
		t.Fatal(err)
	}
	err = captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend := ensureGitAgentBackend(cfg, "git-agent")
		backend.Options["pending"] = map[string]any{
			hash: map[string]any{
				"agent":   "worker-1",
				"expires": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
			},
		}
		cfg.Sandbox.Backends["git-agent"] = backend
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := gitAgentDirectory{backend: "git-agent"}
	if _, err := dir.ConsumeJoinToken(token); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired token must fail, got %v", err)
	}
	// And expiry burns it: a retry is unknown, not expired.
	if _, err := dir.ConsumeJoinToken(token); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("expired token must burn, got %v", err)
	}
}

func TestGitAgentEnrollListRevoke(t *testing.T) {
	isolatedConfig(t)
	dir := gitAgentDirectory{backend: "git-agent"}
	if err := dir.RecordAgent(gitagent.AgentEnrollment{
		Name: "worker-1", Fingerprint: "SHA256:abc",
		URL: "ssh://127.0.0.1:7422/repo.git", HostFingerprint: "SHA256:host",
	}); err != nil {
		t.Fatal(err)
	}
	if name, ok := dir.AgentByFingerprint("SHA256:abc"); !ok || name != "worker-1" {
		t.Fatalf("lookup = %q, %v", name, ok)
	}

	res, err := RunGitAgentList(GitAgentListOptions{Backend: "git-agent"})
	if err != nil {
		t.Fatal(err)
	}
	entries := res.([]GitAgentListEntry)
	if len(entries) != 1 || entries[0].Name != "worker-1" || entries[0].Status != "enrolled" {
		t.Fatalf("entries = %+v", entries)
	}

	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Name: "worker-1", Backend: "git-agent"}); err != nil {
		t.Fatal(err)
	}
	// Revocation is effective for lookups after it (R8.5).
	if _, ok := dir.AgentByFingerprint("SHA256:abc"); ok {
		t.Fatal("revoked fingerprint must be refused")
	}
	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Name: "worker-1", Backend: "git-agent"}); err == nil {
		t.Fatal("revoking an unknown agent must error")
	}
}

func TestGitAgentAddDryRunTouchesNothing(t *testing.T) {
	path := isolatedConfig(t)
	if _, err := RunGitAgentAdd(GitAgentAddOptions{Name: "worker-1", Backend: "git-agent", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not write the config, stat err = %v", err)
	}
	keysDir := filepath.Join(filepath.Dir(path), ".captain", "sandbox")
	if _, err := os.Stat(keysDir); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create key material, stat err = %v", err)
	}
}
