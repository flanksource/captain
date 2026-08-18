package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

// servedBackend reads back what recordServedEndpoint wrote.
func servedBackend(t *testing.T, name string) map[string]any {
	t.Helper()
	cfg, _, err := captainconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sandbox.Backends[name].Options
}

func TestEnrollmentTokenSources(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		opts    GitAgentServeOptions
		want    string
		wantErr string
	}{
		{name: "neither source", opts: GitAgentServeOptions{}},
		{name: "inline", opts: GitAgentServeOptions{Token: text.NewSensitiveString("inline-token")}, want: "inline-token"},
		{name: "file is trimmed", opts: GitAgentServeOptions{TokenFile: tokenFile}, want: "file-token"},
		{
			name:    "both refused rather than ranked",
			opts:    GitAgentServeOptions{Token: text.NewSensitiveString("inline-token"), TokenFile: tokenFile},
			wantErr: "mutually exclusive",
		},
		{name: "empty file", opts: GitAgentServeOptions{TokenFile: empty}, wantErr: "is empty"},
		{name: "missing file", opts: GitAgentServeOptions{TokenFile: filepath.Join(t.TempDir(), "absent")}, wantErr: "--token-file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.opts.enrollmentToken()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Value() != tt.want {
				t.Fatalf("token = %q, want %q", got.Value(), tt.want)
			}
		})
	}
}

// A pool member persists the name it was given so a restart reclaims its slot
// instead of consuming another and eventually exhausting the pool.
func TestEnrolledAgentNameIsRepresentedOnRestart(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	if name := enrolledAgentName(backend); name != "" {
		t.Fatalf("a host that never joined reports the name %q", name)
	}

	err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		b, err := ensureGitAgentBackend(cfg, backend)
		if err != nil {
			return err
		}
		b.Options["supervisor"] = map[string]any{
			"url": "ssh://supervisor:7422", "agent": "prod-pool-02",
		}
		cfg.Sandbox.Backends[backend] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if name := enrolledAgentName(backend); name != "prod-pool-02" {
		t.Fatalf("enrolled agent name = %q, want prod-pool-02", name)
	}
	// An unknown backend has no name rather than a stale one from another.
	if name := enrolledAgentName("other-backend"); name != "" {
		t.Fatalf("unrelated backend reported %q", name)
	}
}

func TestRecordServedEndpointPublishesMailboxIdentity(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"
	opts := GitAgentServeOptions{Backend: backend, Listen: ":7422"}

	if err := recordServedEndpoint(opts, gitagent.RoleMailbox, transportSSH, "/srv/repos", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	options := servedBackend(t, backend)

	// mailboxRoot stays top-level: gitagent.ServedRootFor reads it directly.
	if root, _ := options["mailboxRoot"].(string); root != "/srv/repos" {
		t.Fatalf("mailboxRoot = %q", root)
	}
	want := mailboxRecord{
		Transport: transportSSH, Root: "/srv/repos", Listen: ":7422", Identity: "SHA256:abc", Encrypted: true,
	}
	if got := mailboxRecords(options)[transportSSH]; got != want {
		t.Fatalf("ssh record = %+v, want %+v", got, want)
	}
}

// `captain serve` runs constantly for the web UI. Keying the record by transport
// is what stops it erasing a working ssh mailbox on every start.
func TestServedMailboxRecordsCoexistPerTransport(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	if err := recordServedEndpoint(
		GitAgentServeOptions{Backend: backend, Listen: ":7422"},
		gitagent.RoleMailbox, transportSSH, "/srv/repos", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	if err := recordServedGitMailbox("0.0.0.0:9020", "/srv/repos", nil); err != nil {
		t.Fatal(err)
	}

	records := mailboxRecords(servedBackend(t, backend))
	if len(records) != 2 {
		t.Fatalf("records = %+v, want both transports", records)
	}
	if got := records[transportSSH].Listen; got != ":7422" {
		t.Fatalf("ssh listen = %q; captain serve displaced the ssh mailbox", got)
	}
	// Recorded without a certificate, so detection can say "restart it with
	// --tls" rather than "nothing has ever served here".
	if https := records[transportHTTPS]; https.Encrypted || https.Listen != "0.0.0.0:9020" {
		t.Fatalf("https record = %+v, want an unencrypted record of the real address", https)
	}
}

// Both roles present the same host key, so a probe cannot tell them apart. A
// stale mailbox record claiming an address that now serves a sidecar would send
// deploy's enrollment at the wrong endpoint.
func TestSidecarClearsStaleMailboxRecordForItsOwnAddress(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	mailbox := GitAgentServeOptions{Backend: backend, Listen: ":7422"}
	if err := recordServedEndpoint(mailbox, gitagent.RoleMailbox, transportSSH, "/srv/repos", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}

	elsewhere := GitAgentServeOptions{Backend: backend, Listen: ":7500"}
	if err := recordServedEndpoint(elsewhere, gitagent.RoleSidecar, transportSSH, "/srv/other", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mailboxRecords(servedBackend(t, backend))[transportSSH]; !ok {
		t.Fatal("a sidecar on a different port cleared the mailbox record")
	}

	sameAddress := GitAgentServeOptions{Backend: backend, Listen: ":7422"}
	if err := recordServedEndpoint(sameAddress, gitagent.RoleSidecar, transportSSH, "/srv/other", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mailboxRecords(servedBackend(t, backend))[transportSSH]; ok {
		t.Fatal("stale mailbox record survived a sidecar taking over its address")
	}
}

// An https sidecar and an https mailbox cannot both hold one address, and the
// record is what deploy enrolls against — so the sidecar has to clear the record
// for the protocol it now serves, not for the other one.
func TestHTTPSSidecarClearsTheHTTPSRecordForItsAddress(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	if err := recordServedGitMailbox("0.0.0.0:9020", "/srv/repos", nil); err != nil {
		t.Fatal(err)
	}
	ssh := GitAgentServeOptions{Backend: backend, Listen: ":7422"}
	if err := recordServedEndpoint(ssh, gitagent.RoleMailbox, transportSSH, "/srv/repos", "SHA256:abc"); err != nil {
		t.Fatal(err)
	}

	sidecar := GitAgentServeOptions{Backend: backend, Listen: "0.0.0.0:9020"}
	if err := recordServedEndpoint(sidecar, gitagent.RoleSidecar, transportHTTPS, "/srv/other", ""); err != nil {
		t.Fatal(err)
	}
	records := mailboxRecords(servedBackend(t, backend))
	if _, ok := records[transportHTTPS]; ok {
		t.Fatal("the https mailbox record survived a sidecar taking over its address")
	}
	// The ssh mailbox is a different listener on a different port.
	if _, ok := records[transportSSH]; !ok {
		t.Fatal("an https sidecar cleared an unrelated ssh mailbox record")
	}
}

// Every combination here produces either a listener the supervisor cannot reach
// or a roster entry naming one this process does not serve.
func TestValidateServeTransport(t *testing.T) {
	const advertise = "https://w1.example.com/git/" + SidecarRepoName

	t.Run("defaults to ssh", func(t *testing.T) {
		got, err := validateServeTransport(GitAgentServeOptions{}, gitagent.RoleSidecar)
		if err != nil || got != transportSSH {
			t.Fatalf("transport = %q, err = %v", got, err)
		}
	})

	t.Run("https needs an advertised endpoint", func(t *testing.T) {
		got, err := validateServeTransport(
			GitAgentServeOptions{Transport: "https", Advertise: advertise}, gitagent.RoleSidecar)
		if err != nil || got != transportHTTPS {
			t.Fatalf("transport = %q, err = %v", got, err)
		}
	})

	for _, tc := range []struct {
		name string
		opts GitAgentServeOptions
		role gitagent.ReceiverRole
		want string
	}{{
		name: "an unknown transport names both",
		opts: GitAgentServeOptions{Transport: "quic"}, role: gitagent.RoleSidecar,
		want: "captain speaks ssh and https",
	}, {
		// `captain serve --tls` hosts the https mailbox; it also needs the token
		// store and the web UI, neither of which this command has.
		name: "a mailbox over https points at captain serve",
		opts: GitAgentServeOptions{Transport: "https", Advertise: advertise}, role: gitagent.RoleMailbox,
		want: "captain serve --tls",
	}, {
		// The supervisor would otherwise synthesize an ssh:// URL from the
		// connection and record an endpoint nothing serves.
		name: "https without an advertise is refused",
		opts: GitAgentServeOptions{Transport: "https"}, role: gitagent.RoleSidecar,
		want: "--transport https needs --advertise",
	}, {
		name: "a transport and advertise that disagree are refused",
		opts: GitAgentServeOptions{Transport: "https", Advertise: "ssh://h:7422/repo.git"},
		role: gitagent.RoleSidecar, want: "does not speak",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateServeTransport(tc.opts, tc.role)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %q", err, tc.want)
			}
		})
	}
}

// The certificate persists on the state volume and Covers() is a hard error, so
// a name that changes on reschedule would make the SECOND scheduling of a pod
// fail at startup with no way in to delete the file.
func TestSidecarCertificateCoversOnlyTheAdvertisedName(t *testing.T) {
	host, err := advertiseHostname("https://w1.example.com/git/" + SidecarRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if host != "w1.example.com" {
		t.Fatalf("hostname = %q", host)
	}

	credential, err := gitagent.EnsureTLSCredential(t.TempDir(), []string{host})
	if err != nil {
		t.Fatal(err)
	}
	if err := credential.Covers([]string{host}); err != nil {
		t.Fatalf("the certificate does not cover the name it was issued for: %v", err)
	}
	// A pod IP is exactly the kind of name that must NOT be a requirement, or a
	// reschedule onto a new address would refuse to start.
	if err := credential.Covers([]string{"10.1.2.3"}); err == nil {
		t.Fatal("the certificate claims to cover a pod IP, which changes on every reschedule")
	}

	if _, err := advertiseHostname("not a url"); err == nil {
		t.Fatal("a malformed advertise was accepted as a certificate name")
	}
}

// deploy needs the token in-process; the HTTP surface must not gain it.
func TestGitAgentAddExposesTokenToCallersButNotToJSON(t *testing.T) {
	isolatedConfig(t)
	gitAgentTokenDB(t)

	res, err := RunGitAgentAdd(t.Context(), GitAgentAddOptions{Name: "worker-1", Backend: "git-agent"})
	if err != nil {
		t.Fatal(err)
	}
	add, ok := res.(GitAgentAddResult)
	if !ok {
		t.Fatalf("result = %T", res)
	}

	token := add.Token.Value()
	if token == "" {
		t.Fatal("no token returned; deploy would have to re-parse JoinCommand")
	}
	if !strings.Contains(add.JoinCommand, "--token "+token) {
		t.Fatalf("Token %q is not the one in JoinCommand %q", token, add.JoinCommand)
	}

	encoded, err := json.Marshal(add)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"token"`) {
		t.Fatalf("token field crossed the JSON boundary: %s", encoded)
	}
}

// The supervisor records this string and pushes to it verbatim, so a repository
// path that is dropped here is a 403 on every dispatch and nothing earlier.
func TestAdvertiseURL(t *testing.T) {
	for given, want := range map[string]string{
		"":                        "",
		"host:7422":               "ssh://host:7422/" + SidecarRepoName,
		"captain@1.2.3.4:9":       "ssh://captain@1.2.3.4:9/" + SidecarRepoName,
		"ssh://h:1/other.git":     "ssh://h:1/other.git",
		"ssh://h:1/":              "ssh://h:1/" + SidecarRepoName,
		"https://a.example.com":   "https://a.example.com/git/" + SidecarRepoName,
		"https://a.example.com/":  "https://a.example.com/git/" + SidecarRepoName,
		"https://a.example.com:8": "https://a.example.com:8/git/" + SidecarRepoName,
		"https://a.example.com/git/" + SidecarRepoName: "https://a.example.com/git/" + SidecarRepoName,
	} {
		got, err := advertiseURL(given)
		if err != nil {
			t.Errorf("advertiseURL(%q) errored: %v", given, err)
			continue
		}
		if got != want {
			t.Errorf("advertiseURL(%q) = %q, want %q", given, got, want)
		}
	}

	// awaitEnrollment compares the recorded URL to plan.Advertise byte for byte,
	// and the sidecar re-normalizes what deploy passed it through this same
	// function. If that is not a fixed point, every https deploy fails with
	// "enrolled advertising X, but the deployment expects Y".
	for _, settled := range []string{
		"ssh://captain@127.0.0.1:7423/" + SidecarRepoName,
		"https://worker-01.agents.example.com/git/" + SidecarRepoName,
	} {
		got, err := advertiseURL(settled)
		if err != nil || got != settled {
			t.Errorf("advertiseURL(%q) = %q, %v; want it unchanged", settled, got, err)
		}
	}

	// http:// would put a bearer token on the wire in clear text, and a scheme
	// captain does not speak has to be refused rather than pushed to.
	for _, refused := range []string{"http://h", "git://h/repo.git", "ssh://"} {
		if _, err := advertiseURL(refused); err == nil {
			t.Errorf("advertiseURL(%q) was accepted", refused)
		}
	}
}
