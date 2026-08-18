package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
)

// recordedAgent reads back what RecordAgent wrote for one agent.
func recordedAgent(t *testing.T, backend, name string) map[string]any {
	t.Helper()
	cfg, _, err := captainconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := enrolledAgent(cfg, backend, name)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// Which credential the supervisor must present is decided by the endpoint's
// scheme, and recording an agent it can reach but not authenticate to would
// produce a roster that looks complete and fails at the first dispatch.
func TestRecordAgentRequiresTheCredentialItsTransportUses(t *testing.T) {
	const backend = "git-agent"
	const secret = "cptn_aaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	t.Run("an ssh agent is pinned by host key", func(t *testing.T) {
		isolatedConfig(t)
		directory := gitAgentDirectory{backend: backend}

		err := directory.RecordAgent(gitagent.AgentEnrollment{
			Name: "w1", URL: "ssh://captain@h:7422/repo.git", HostFingerprint: "SHA256:abc",
		})
		if err != nil {
			t.Fatal(err)
		}
		entry := recordedAgent(t, backend, "w1")
		if entry["hostFingerprint"] != "SHA256:abc" {
			t.Fatalf("hostFingerprint = %v", entry["hostFingerprint"])
		}
		if _, ok := entry["tokenPath"]; ok {
			t.Fatal("an ssh agent was given a bearer token it does not use")
		}
	})

	t.Run("an https agent authenticates by token", func(t *testing.T) {
		isolatedConfig(t)
		directory := gitAgentDirectory{backend: backend}

		err := directory.RecordAgent(gitagent.AgentEnrollment{
			Name: "w1", URL: "https://w1.example.com/git/repo.git", DispatchToken: secret,
		})
		if err != nil {
			t.Fatal(err)
		}
		entry := recordedAgent(t, backend, "w1")
		if _, ok := entry["hostFingerprint"]; ok {
			t.Fatal("an https agent was pinned by a host key it does not present")
		}
		path, _ := entry["tokenPath"].(string)
		if path == "" {
			t.Fatal("no token path was recorded")
		}
		token, err := gitagent.ReadTokenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if token.Value() != secret {
			t.Fatalf("stored token = %q", token.Value())
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("token mode = %v, want 0600", info.Mode().Perm())
		}
	})

	// ~/.captain.yaml has no guaranteed mode, is read by hook shims running as
	// whoever pushed, and is echoed in dry-run output.
	t.Run("the secret never enters the config file", func(t *testing.T) {
		path := isolatedConfig(t)
		directory := gitAgentDirectory{backend: backend}

		err := directory.RecordAgent(gitagent.AgentEnrollment{
			Name: "w1", URL: "https://w1.example.com/git/repo.git", DispatchToken: secret,
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the dispatch token was written into %s", path)
		}
	})

	t.Run("refusals name the fix", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			enrollment gitagent.AgentEnrollment
			want       string
		}{{
			name:       "no endpoint at all",
			enrollment: gitagent.AgentEnrollment{Name: "w1"},
			want:       "advertised no endpoint",
		}, {
			name:       "ssh without a host key",
			enrollment: gitagent.AgentEnrollment{Name: "w1", URL: "ssh://h:7422/repo.git"},
			want:       "advertised no host key fingerprint",
		}, {
			// No cross-transport leniency: a token proves nothing to an ssh push.
			name: "ssh carrying only a token",
			enrollment: gitagent.AgentEnrollment{
				Name: "w1", URL: "ssh://h:7422/repo.git", DispatchToken: secret,
			},
			want: "advertised no host key fingerprint",
		}, {
			name:       "https without a token",
			enrollment: gitagent.AgentEnrollment{Name: "w1", URL: "https://w1.example.com/git/repo.git"},
			want:       "--transport https",
		}, {
			// And the mirror: a host key is not something an https client checks.
			name: "https carrying only a host key",
			enrollment: gitagent.AgentEnrollment{
				Name: "w1", URL: "https://w1.example.com/git/repo.git", HostFingerprint: "SHA256:abc",
			},
			want: "issued no dispatch token",
		}, {
			name:       "a scheme captain does not speak",
			enrollment: gitagent.AgentEnrollment{Name: "w1", URL: "git://h/repo.git", HostFingerprint: "SHA256:abc"},
			want:       "not a transport captain speaks",
		}} {
			t.Run(tc.name, func(t *testing.T) {
				isolatedConfig(t)
				err := gitAgentDirectory{backend: backend}.RecordAgent(tc.enrollment)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want it to name %q", err, tc.want)
				}
			})
		}
	})

	// An entry carrying both credentials would look like it authenticates two
	// ways, and the stale one would outlive the transport that used it.
	t.Run("re-enrolling across transports drops the stale credential", func(t *testing.T) {
		isolatedConfig(t)
		directory := gitAgentDirectory{backend: backend}

		if err := directory.RecordAgent(gitagent.AgentEnrollment{
			Name: "w1", URL: "ssh://captain@h:7422/repo.git", HostFingerprint: "SHA256:abc",
		}); err != nil {
			t.Fatal(err)
		}
		if err := directory.RecordAgent(gitagent.AgentEnrollment{
			Name: "w1", URL: "https://w1.example.com/git/repo.git", DispatchToken: secret,
		}); err != nil {
			t.Fatal(err)
		}
		entry := recordedAgent(t, backend, "w1")
		if _, ok := entry["hostFingerprint"]; ok {
			t.Fatal("the ssh host key survived a re-enrollment over https")
		}
		if _, ok := entry["tokenPath"]; !ok {
			t.Fatal("the https token was not recorded")
		}
	})
}

// A revoked agent whose credential is still on disk is still a way in.
func TestRevokeRemovesTheDispatchToken(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"
	const secret = "cptn_aaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	err := gitAgentDirectory{backend: backend}.RecordAgent(gitagent.AgentEnrollment{
		Name: "w1", URL: "https://w1.example.com/git/repo.git", DispatchToken: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := recordedAgent(t, backend, "w1")["tokenPath"].(string)

	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Backend: backend, Name: "w1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the dispatch token survived revocation at %s (err = %v)", path, err)
	}
}

// Revoking an ssh agent has no token to remove, and must not fail looking.
func TestRevokeWithoutADispatchTokenSucceeds(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	err := gitAgentDirectory{backend: backend}.RecordAgent(gitagent.AgentEnrollment{
		Name: "w1", URL: "ssh://captain@h:7422/repo.git", HostFingerprint: "SHA256:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Backend: backend, Name: "w1"}); err != nil {
		t.Fatal(err)
	}
}

// The keys directory follows the config path, so a test never writes a
// credential into the developer's real home.
func TestDispatchTokensStayInsideTheIsolatedKeysDir(t *testing.T) {
	configPath := isolatedConfig(t)
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(keysDir, filepath.Dir(configPath)) {
		t.Fatalf("keys dir %s escaped the isolated config at %s", keysDir, configPath)
	}
}
