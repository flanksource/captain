package cli

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
	gossh "golang.org/x/crypto/ssh"
)

// serveHostKey starts an SSH listener presenting signer, standing in for a
// mailbox: detection aborts at the key exchange and never issues a command.
func serveHostKey(t *testing.T, signer gossh.Signer) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	config := &gossh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _, _, _ = gossh.NewServerConn(conn, config)
			}()
		}
	}()
	return listener
}

// recordMailbox writes the record a serving process would.
func recordMailbox(t *testing.T, backend string, record mailboxRecord) {
	t.Helper()
	err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		b, err := ensureGitAgentBackend(cfg, backend)
		if err != nil {
			return err
		}
		setMailboxRecord(b.Options, record)
		cfg.Sandbox.Backends[backend] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// recordMailboxListening writes an ssh mailbox record for the address given.
func recordMailboxListening(t *testing.T, backend, listen string) {
	t.Helper()
	recordMailbox(t, backend, mailboxRecord{Transport: transportSSH, Listen: listen, Encrypted: true})
}

// serveTLSPresenting starts an HTTPS listener holding a captain-generated
// certificate, and returns its listen address and public-key pin — what
// `captain serve --tls` records about itself.
func serveTLSPresenting(t *testing.T) (listen, pin string) {
	t.Helper()
	credential, err := gitagent.EnsureTLSCredential(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	server.TLS = &tls.Config{Certificates: []tls.Certificate{credential.Certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.Listener.Addr().String(), credential.PublicKeyPin
}

// hostSigner returns this host's git-agent host key, creating it as serve would.
func hostSigner(t *testing.T) gossh.Signer {
	t.Helper()
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		t.Fatal(err)
	}
	signer, _, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, hostKeyName))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// Without a record there is no address to enroll against. Both the
// no-such-backend and the backend-never-served paths must refuse and name both
// ways to fix it, rather than falling back to a hardcoded :7422 — that guess is
// exactly what the record replaces.
func TestDetectMailboxRequiresARecord(t *testing.T) {
	for _, tc := range []struct{ name, backend string }{
		{"backend does not exist", "git-agent"},
		{"backend exists but never served", "configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedConfig(t)
			if tc.backend == "configured" {
				if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
					_, err := ensureGitAgentBackend(cfg, tc.backend)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}

			_, err := detectMailbox(t.Context(), mailboxDetection{Backend: tc.backend})
			if err == nil {
				t.Fatal("detection succeeded with no recorded mailbox")
			}
			// Both transports host a mailbox, so a refusal that named only one
			// would send an operator to start a process they do not need.
			for _, want := range []string{"captain serve", "serve --role mailbox"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want it to name %q", err, want)
				}
			}
			if strings.Contains(err.Error(), ":7422") {
				t.Fatalf("a missing record must not resolve to a default port: %v", err)
			}
		})
	}
}

func TestDetectMailboxProvesTheListenerIsOurs(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	listener := serveHostKey(t, hostSigner(t))
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	recordMailboxListening(t, backend, ":"+port)

	mailbox, err := detectMailbox(t.Context(), mailboxDetection{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.Port != atoi(t, port) {
		t.Fatalf("port = %d, want %s", mailbox.Port, port)
	}
	if mailbox.Transport != transportSSH {
		t.Fatalf("transport = %q, want ssh", mailbox.Transport)
	}
	if mailbox.HostFingerprint == "" {
		t.Fatal("no host fingerprint; the sidecar would have nothing to pin")
	}
}

// The https half of the same proof: `captain serve --tls` records its pin, and
// detection confirms the address is still held by the server that recorded it.
func TestDetectMailboxProvesAnHTTPSListenerIsOurs(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	listen, pin := serveTLSPresenting(t)
	recordMailbox(t, backend, mailboxRecord{
		Transport: transportHTTPS, Listen: listen, Identity: pin, Encrypted: true,
	})

	mailbox, err := detectMailbox(t.Context(), mailboxDetection{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.Transport != transportHTTPS {
		t.Fatalf("transport = %q, want https", mailbox.Transport)
	}
	if mailbox.HostFingerprint != pin {
		t.Fatalf("identity = %q, want the served pin %q", mailbox.HostFingerprint, pin)
	}
}

// A TCP dial would call an unrelated sshd healthy, and enrolling against it
// hands a durable credential to a server that is not the supervisor.
func TestDetectMailboxRejectsAForeignSSHServer(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	foreign, _, err := gitagent.EnsureKeyPair(filepath.Join(t.TempDir(), "foreign_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	listener := serveHostKey(t, foreign)
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	recordMailboxListening(t, backend, ":"+port)

	_, err = detectMailbox(t.Context(), mailboxDetection{Backend: backend})
	if err == nil || !strings.Contains(err.Error(), "another server holds that address") {
		t.Fatalf("err = %v, want a host-key mismatch", err)
	}
}

func TestDetectMailboxRefusesALoopbackBindForOffHostWorkloads(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"
	recordMailboxListening(t, backend, "127.0.0.1:7422")

	_, err := detectMailbox(t.Context(), mailboxDetection{Backend: backend, NeedOffHost: true})
	if err == nil || !strings.Contains(err.Error(), "no container or pod can reach") {
		t.Fatalf("err = %v, want a refusal to enroll against a loopback-bound mailbox", err)
	}
	if !strings.Contains(err.Error(), "--listen :7422") {
		t.Fatalf("err = %v, want the ssh mailbox's own restart flag", err)
	}
}

// This is the refusal a default `captain serve` earns: it hosts the mailbox, so
// there IS a record, but on loopback and over plain HTTP. Reporting "no mailbox
// has ever served here" would send the operator to start a process they already
// have running.
func TestDetectMailboxRefusesAPlainHTTPServe(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"
	recordMailbox(t, backend, mailboxRecord{Transport: transportHTTPS, Listen: "localhost:9020"})

	_, err := detectMailbox(t.Context(), mailboxDetection{Backend: backend, NeedOffHost: true})
	if err == nil {
		t.Fatal("detection accepted a mailbox that would carry a token in clear text")
	}
	for _, want := range []string{"plain HTTP", "--tls", "--host 0.0.0.0", "localhost:9020"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to name %q", err, want)
		}
	}
}

func TestDetectMailboxRefusesALoopbackBoundServe(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"
	recordMailbox(t, backend, mailboxRecord{
		Transport: transportHTTPS, Listen: "127.0.0.1:9020", Identity: "sha256//x", Encrypted: true,
	})

	_, err := detectMailbox(t.Context(), mailboxDetection{Backend: backend, NeedOffHost: true})
	if err == nil || !strings.Contains(err.Error(), "--host 0.0.0.0") {
		t.Fatalf("err = %v, want the flag that rebinds captain serve", err)
	}
	if strings.Contains(err.Error(), "--listen") {
		t.Fatalf("err = %v names the ssh mailbox's flag for an https mailbox", err)
	}
}

// `captain serve` runs constantly for the web UI, so it must not displace a
// working ssh mailbox — and when it is the usable one, it wins.
func TestSelectMailboxRecordPrefersAUsableHTTPS(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"
	recordMailboxListening(t, backend, ":7422")

	t.Run("plain-HTTP serve does not displace a working ssh mailbox", func(t *testing.T) {
		recordMailbox(t, backend, mailboxRecord{Transport: transportHTTPS, Listen: "localhost:9020"})

		record, err := selectMailboxRecord(backend, "")
		if err != nil {
			t.Fatal(err)
		}
		if record.Transport != transportSSH {
			t.Fatalf("transport = %q, want the ssh mailbox that still works", record.Transport)
		}
	})

	t.Run("a TLS serve is preferred, needing no second process", func(t *testing.T) {
		recordMailbox(t, backend, mailboxRecord{
			Transport: transportHTTPS, Listen: "0.0.0.0:9020", Identity: "sha256//x", Encrypted: true,
		})

		record, err := selectMailboxRecord(backend, "")
		if err != nil {
			t.Fatal(err)
		}
		if record.Transport != transportHTTPS {
			t.Fatalf("transport = %q, want https", record.Transport)
		}
	})

	t.Run("--transport forces the other one", func(t *testing.T) {
		record, err := selectMailboxRecord(backend, transportSSH)
		if err != nil {
			t.Fatal(err)
		}
		if record.Listen != ":7422" {
			t.Fatalf("listen = %q, want the ssh mailbox", record.Listen)
		}
	})

	t.Run("--transport names what is recorded when it asks for what is not", func(t *testing.T) {
		isolatedConfig(t)
		recordMailboxListening(t, backend, ":7422")

		_, err := selectMailboxRecord(backend, transportHTTPS)
		if err == nil || !strings.Contains(err.Error(), "recorded: ssh") {
			t.Fatalf("err = %v, want it to say which transport IS recorded", err)
		}
	})
}

func TestResolveSupervisorAddress(t *testing.T) {
	mailbox := detectedMailbox{
		Transport: transportSSH, Port: 7422,
		OffHostAddresses: []string{"192.168.1.20", "172.17.0.1"},
	}

	t.Run("docker reaches the host through the gateway alias", func(t *testing.T) {
		address, source, err := resolveSupervisorAddress(deploy.TargetDocker, mailbox, "")
		if err != nil {
			t.Fatal(err)
		}
		if address != "ssh://captain@host.docker.internal:7422" || source != "docker-host-gateway" {
			t.Fatalf("address = %q, source = %q", address, source)
		}
	})

	// The same alias, but the URL the HTTPS transport takes: no user, and the
	// repository path is appended per-push rather than baked in here.
	t.Run("an https mailbox yields an https supervisor URL", func(t *testing.T) {
		served := detectedMailbox{Transport: transportHTTPS, Port: 9020}
		address, source, err := resolveSupervisorAddress(deploy.TargetDocker, served, "")
		if err != nil {
			t.Fatal(err)
		}
		if address != "https://host.docker.internal:9020" || source != "docker-host-gateway" {
			t.Fatalf("address = %q, source = %q", address, source)
		}
	})

	// A laptop's LAN address is almost never routable from a managed cluster.
	// Guessing yields a pod that crash-loops after the token is already spent.
	t.Run("kubernetes refuses to guess a route into the cluster", func(t *testing.T) {
		_, _, err := resolveSupervisorAddress(deploy.TargetKubernetes, mailbox, "")
		if err == nil || !strings.Contains(err.Error(), "--supervisor-address") {
			t.Fatalf("err = %v, want a demand for an explicit address", err)
		}
		// The operator has to pick one, so every address that answered is named
		// rather than only the routing table's guess.
		for _, want := range []string{"ssh://192.168.1.20:7422", "ssh://172.17.0.1:7422"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("an explicit address wins and keeps its port", func(t *testing.T) {
		address, source, err := resolveSupervisorAddress(deploy.TargetKubernetes, mailbox, "ssh://mailbox.internal:2222")
		if err != nil {
			t.Fatal(err)
		}
		if address != "ssh://mailbox.internal:2222" || source != "flag" {
			t.Fatalf("address = %q, source = %q", address, source)
		}
	})

	// A portless endpoint defaults to :22 over ssh and :443 over https, so a bare
	// host would otherwise probe sshd or an unrelated web server, not the mailbox.
	t.Run("a portless address gets the mailbox port, not the scheme default", func(t *testing.T) {
		for given, want := range map[string]string{
			"mailbox.internal":         "ssh://mailbox.internal:7422",
			"ssh://mailbox.internal":   "ssh://mailbox.internal:7422",
			"https://mailbox.internal": "https://mailbox.internal:7422",
		} {
			address, _, err := resolveSupervisorAddress(deploy.TargetDocker, mailbox, given)
			if err != nil {
				t.Fatal(err)
			}
			if address != want {
				t.Errorf("resolve(%q) = %q, want %q", given, address, want)
			}
		}
	})
}

// A certificate that omits the name the agent dials produces a sidecar that
// enrolls and then fails every relay with a TLS error naming neither cause nor
// fix, so it is proven here against the certificate actually being served.
func TestVerifySupervisorNameIsCovered(t *testing.T) {
	listen, pin := serveTLSPresenting(t)
	mailbox := detectedMailbox{Transport: transportHTTPS, Listen: listen, HostFingerprint: pin}

	if err := verifySupervisorNameIsCovered(t.Context(), mailbox, "https://host.docker.internal:9020"); err != nil {
		t.Fatalf("a generated certificate must cover the name docker sidecars dial: %v", err)
	}

	err := verifySupervisorNameIsCovered(t.Context(), mailbox, "https://supervisor.corp:9020")
	if err == nil || !strings.Contains(err.Error(), "--tls-host supervisor.corp") {
		t.Fatalf("err = %v, want the flag that would cover the name", err)
	}

	// An ssh mailbox has no certificate, so there is nothing to check rather
	// than something that fails.
	if err := verifySupervisorNameIsCovered(t.Context(), detectedMailbox{Transport: transportSSH}, "ssh://x:22"); err != nil {
		t.Fatalf("ssh must not be certificate-checked: %v", err)
	}
}

func TestResolveAdvertiseAddress(t *testing.T) {
	plan := deploy.Plan{Name: "worker-01", ListenPort: 7422, HostPort: 7423}

	t.Run("docker advertises its published loopback port", func(t *testing.T) {
		address, source, err := resolveAdvertiseAddress(deploy.TargetDocker, plan, "", "", false)
		if err != nil {
			t.Fatal(err)
		}
		want := "ssh://captain@127.0.0.1:7423/" + SidecarRepoName
		if address != want || source != "docker-published-port" {
			t.Fatalf("address = %q, source = %q, want %q", address, source, want)
		}
	})

	// A pod IP changes on restart, and RecordAgent stores the URL once at
	// enrollment, so the Service name is the only durable answer — but only a
	// supervisor inside the cluster can route to it.
	t.Run("an in-cluster supervisor advertises the Service, not a pod IP", func(t *testing.T) {
		address, source, err := resolveAdvertiseAddress(deploy.TargetKubernetes, plan, "agents", "", true)
		if err != nil {
			t.Fatal(err)
		}
		want := "ssh://captain@captain-git-agent-worker-01.agents.svc.cluster.local:7422/" + SidecarRepoName
		if address != want || source != "cluster-service" {
			t.Fatalf("address = %q, source = %q, want %q", address, source, want)
		}
	})

	// A ClusterIP address the supervisor cannot route to would enroll, look
	// healthy, and never receive a dispatch.
	t.Run("an out-of-cluster supervisor refuses to advertise a cluster address", func(t *testing.T) {
		_, _, err := resolveAdvertiseAddress(deploy.TargetKubernetes, plan, "agents", "", false)
		if err == nil || !strings.Contains(err.Error(), "--domain") {
			t.Fatalf("err = %v, want a demand for a route the supervisor can reach", err)
		}
	})

	t.Run("an ingress advertises https under the git prefix", func(t *testing.T) {
		routed := plan
		routed.ExternalRoute = deploy.ExternalRoute{Host: "worker-01.agents.example.com", ClassName: "nginx"}

		address, source, err := resolveAdvertiseAddress(deploy.TargetKubernetes, routed, "agents", "", false)
		if err != nil {
			t.Fatal(err)
		}
		want := "https://worker-01.agents.example.com/git/" + SidecarRepoName
		if address != want || source != "cluster-ingress" {
			t.Fatalf("address = %q, source = %q, want %q", address, source, want)
		}

		// awaitEnrollment compares the recorded URL to plan.Advertise byte for
		// byte, and the sidecar re-normalizes what it was passed through the same
		// advertiseURL. If that is not a fixed point every ingress deploy fails
		// with "enrolled advertising X, but the deployment expects Y".
		settled, err := advertiseURL(address)
		if err != nil || settled != address {
			t.Fatalf("advertiseURL(%q) = %q, %v; want it unchanged", address, settled, err)
		}
	})

	t.Run("docker refuses to advertise before a port is published", func(t *testing.T) {
		_, _, err := resolveAdvertiseAddress(deploy.TargetDocker, deploy.Plan{Name: "w"}, "", "", false)
		if err == nil || !strings.Contains(err.Error(), "published host port") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"":          false, // ":7422" binds every interface
		"0.0.0.0":   false,
		"::":        false,
		"127.0.0.1": true,
		"::1":       true,
		"localhost": true,
		"10.0.0.4":  false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}
