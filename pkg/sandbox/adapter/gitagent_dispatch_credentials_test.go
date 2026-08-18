package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

const testDispatchToken = "cptn_aaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// Which credential is required is decided by the agent's own URL scheme, so an
// agent the supervisor can reach but not authenticate to is refused here rather
// than at the first push.
func TestDispatchCredentials(t *testing.T) {
	t.Run("an ssh agent dispatches with its pinned host key", func(t *testing.T) {
		hostFP, token, err := dispatchCredentials("w1", "ssh://captain@h:7422/repo.git",
			map[string]any{"hostFingerprint": "SHA256:abc"})
		if err != nil {
			t.Fatal(err)
		}
		if hostFP != "SHA256:abc" {
			t.Fatalf("hostFingerprint = %q", hostFP)
		}
		if !token.IsEmpty() {
			t.Fatal("an ssh dispatch was given a bearer token it does not send")
		}
	})

	t.Run("an https agent dispatches with its bearer token", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "w1.token")
		if err := gitagent.WriteTokenFile(path, text.NewSensitiveString(testDispatchToken)); err != nil {
			t.Fatal(err)
		}
		hostFP, token, err := dispatchCredentials("w1", "https://w1.example.com/git/repo.git",
			map[string]any{"tokenPath": path})
		if err != nil {
			t.Fatal(err)
		}
		if token.Value() != testDispatchToken {
			t.Fatalf("token = %q", token.Value())
		}
		// The ingress presents a publicly trusted certificate for the agent's own
		// name, so there is nothing to pin and no host key to compare.
		if hostFP != "" {
			t.Fatalf("hostFingerprint = %q, want none for https", hostFP)
		}
	})

	t.Run("refusals name the agent and the fix", func(t *testing.T) {
		for _, tc := range []struct {
			name, url string
			entry     map[string]any
			want      string
		}{{
			name: "ssh with no host key", url: "ssh://h:7422/repo.git",
			entry: map[string]any{}, want: "no host key recorded",
		}, {
			name: "https with no token path", url: "https://w1.example.com/git/repo.git",
			entry: map[string]any{}, want: "issued this supervisor no dispatch token",
		}, {
			// A host key proves nothing about an HTTPS endpoint, so carrying only
			// one must not be mistaken for a usable credential.
			name: "https carrying only a host key", url: "https://w1.example.com/git/repo.git",
			entry: map[string]any{"hostFingerprint": "SHA256:abc"}, want: "no dispatch token",
		}, {
			name: "https whose token file is gone", url: "https://w1.example.com/git/repo.git",
			entry: map[string]any{"tokenPath": "/nonexistent/w1.token"}, want: "/nonexistent/w1.token",
		}, {
			name: "a scheme captain does not speak", url: "git://h/repo.git",
			entry: map[string]any{}, want: "not a transport captain speaks",
		}} {
			t.Run(tc.name, func(t *testing.T) {
				_, _, err := dispatchCredentials("w1", tc.url, tc.entry)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want it to name %q", err, tc.want)
				}
			})
		}
	})
}

// The token rides a DispatchRequest that error paths format with %w around
// values that may be printed. text.SensitiveString redacting under every verb is
// what keeps it out of logs, so it is asserted rather than assumed.
func TestDispatchRequestRedactsTheToken(t *testing.T) {
	req := gitagent.DispatchRequest{
		SidecarURL: "https://w1.example.com/git/repo.git",
		Token:      text.NewSensitiveString(testDispatchToken),
	}
	for _, rendered := range []string{
		fmt.Sprintf("%v", req), fmt.Sprintf("%+v", req), fmt.Sprintf("%s", req.Token), req.Token.String(),
	} {
		if strings.Contains(rendered, testDispatchToken) {
			t.Fatalf("the dispatch token survived formatting: %s", rendered)
		}
	}
}

// The supervisor holds this credential at rest, so its file must be no more
// readable than the ssh key it replaces.
func TestDispatchTokenFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w1.token")
	if err := gitagent.WriteTokenFile(path, text.NewSensitiveString(testDispatchToken)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}
