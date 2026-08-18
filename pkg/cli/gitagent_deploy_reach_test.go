package cli

import (
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/gitagent"
	gossh "golang.org/x/crypto/ssh"
)

func ips(t *testing.T, texts ...string) []net.IP {
	t.Helper()
	parsed := make([]net.IP, 0, len(texts))
	for _, text := range texts {
		ip := net.ParseIP(text)
		if ip == nil {
			t.Fatalf("bad test address %q", text)
		}
		parsed = append(parsed, ip)
	}
	return parsed
}

func TestOffHostCandidates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bindHost string
		primary  string
		held     []string
		want     []string
	}{{
		// Every other address is a guaranteed failure line, because a listener
		// bound to one address answers there and nowhere else.
		name: "a specific bind address is the only candidate", bindHost: "192.168.1.20",
		primary: "10.8.0.2", held: []string{"10.8.0.2", "192.168.1.20", "172.17.0.1"},
		want: []string{"192.168.1.20"},
	}, {
		name: "the routing table's answer ranks first", bindHost: "",
		primary: "192.168.1.20", held: []string{"172.17.0.1", "192.168.1.20"},
		want: []string{"192.168.1.20", "172.17.0.1"},
	}, {
		// The offline-laptop refusal: primaryOutboundAddress finds nothing, but
		// a container still reaches the host on the bridge gateway.
		name: "no default route still yields the bridge", bindHost: "",
		primary: "", held: []string{"172.17.0.1"},
		want: []string{"172.17.0.1"},
	}, {
		// The reason this change exists: the tunnel address is first because the
		// routing table named it, but it no longer hides the addresses that work.
		name: "a VPN primary does not hide the LAN", bindHost: "",
		primary: "10.8.0.2", held: []string{"10.8.0.2", "192.168.1.20", "172.17.0.1"},
		want: []string{"10.8.0.2", "172.17.0.1", "192.168.1.20"},
	}, {
		name: "unreachable address families are never candidates", bindHost: "",
		primary: "", held: []string{"127.0.0.1", "::1", "169.254.10.1", "fe80::1", "224.0.0.1", "192.168.1.20"},
		want: []string{"192.168.1.20"},
	}, {
		// An IPv4 wildcard socket can never accept an IPv6 connection.
		name: "an 0.0.0.0 bind drops IPv6 candidates", bindHost: "0.0.0.0",
		primary: "", held: []string{"192.168.1.20", "2001:db8::5"},
		want: []string{"192.168.1.20"},
	}, {
		name: "a wildcard bind keeps both families, IPv4 first", bindHost: "::",
		primary: "", held: []string{"2001:db8::5", "192.168.1.20"},
		want: []string{"192.168.1.20", "2001:db8::5"},
	}, {
		name: "the primary is not repeated when it is also held", bindHost: "",
		primary: "192.168.1.20", held: []string{"192.168.1.20"},
		want: []string{"192.168.1.20"},
	}, {
		// A loopback bind reaches refuseUnusableMailbox first, but enumeration
		// must not treat it as a pinned single candidate if it ever gets here.
		name: "a loopback bind falls through to enumeration", bindHost: "127.0.0.1",
		primary: "", held: []string{"192.168.1.20"},
		want: []string{"192.168.1.20"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := offHostCandidates(tc.bindHost, tc.primary, ips(t, tc.held...))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("candidates = %v, want %v", got, tc.want)
			}
		})
	}

	// net.InterfaceAddrs returns kernel order. A refusal that lists candidates
	// in a different order on each run cannot be diffed against the last one.
	t.Run("order does not depend on the order the kernel reported", func(t *testing.T) {
		forward := offHostCandidates("", "", ips(t, "192.168.1.20", "172.17.0.1", "2001:db8::5"))
		reversed := offHostCandidates("", "", ips(t, "2001:db8::5", "172.17.0.1", "192.168.1.20"))
		if !reflect.DeepEqual(forward, reversed) {
			t.Fatalf("%v != %v", forward, reversed)
		}
	})
}

// sshMailboxOn records and serves an ssh mailbox holding this host's own key,
// and returns the detectedMailbox a caller would have proven on loopback.
func sshMailboxOn(t *testing.T, signer gossh.Signer) detectedMailbox {
	t.Helper()
	listener := serveHostKey(t, signer)
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	_, fingerprint, err := gitagent.EnsureKeyPair(filepath.Join(mustKeysDir(t), hostKeyName))
	if err != nil {
		t.Fatal(err)
	}
	return detectedMailbox{Transport: transportSSH, Port: atoi(t, port), HostFingerprint: fingerprint}
}

func mustKeysDir(t *testing.T) string {
	t.Helper()
	dir, err := gitAgentKeysDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProveReachableOffHost(t *testing.T) {
	// The candidate list is a parameter precisely so this can run on a sandboxed
	// host, which cannot serve or reach a genuine off-loopback address.
	t.Run("a failing higher-ranked candidate does not abort the run", func(t *testing.T) {
		isolatedConfig(t)
		mailbox := sshMailboxOn(t, hostSigner(t))

		// 192.0.2.1 is TEST-NET-1 and never answers; it must not stop the probe
		// of the candidate behind it, which is the whole point of the change.
		reachable, err := proveReachableOffHost(t.Context(), mailbox, []string{"192.0.2.1", "127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(reachable, []string{"127.0.0.1"}) {
			t.Fatalf("reachable = %v, want only the address that answered", reachable)
		}
	})

	// Probes run concurrently, so whichever answers first would otherwise decide
	// the reported address and the result would differ between runs.
	t.Run("rank decides the order, not which probe returned first", func(t *testing.T) {
		isolatedConfig(t)
		mailbox := sshMailboxOn(t, hostSigner(t))

		for _, candidates := range [][]string{{"localhost", "127.0.0.1"}, {"127.0.0.1", "localhost"}} {
			reachable, err := proveReachableOffHost(t.Context(), mailbox, candidates)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reachable, candidates) {
				t.Fatalf("reachable = %v, want the candidate order %v", reachable, candidates)
			}
		}
	})

	// A TCP dial would call an unrelated sshd reachable. Collecting every result
	// concurrently must not turn a mismatch into a success.
	t.Run("an identity mismatch is a failure, not a reachable address", func(t *testing.T) {
		isolatedConfig(t)
		foreign, _, err := gitagent.EnsureKeyPair(filepath.Join(t.TempDir(), "foreign_ed25519"))
		if err != nil {
			t.Fatal(err)
		}
		mailbox := sshMailboxOn(t, foreign)

		_, err = proveReachableOffHost(t.Context(), mailbox, []string{"127.0.0.1"})
		if err == nil || !strings.Contains(err.Error(), "another server holds that address") {
			t.Fatalf("err = %v, want a host-key mismatch", err)
		}
	})

	t.Run("no candidate answering names every one and the escape hatch", func(t *testing.T) {
		isolatedConfig(t)
		dead, err := freeLoopbackPort(0)
		if err != nil {
			t.Fatal(err)
		}
		mailbox := detectedMailbox{Transport: transportSSH, Port: dead, HostFingerprint: "SHA256:absent"}

		// TEST-NET-1 rather than a plausible private address: a CI host that
		// happens to sit on the same /24 would make this dial hang.
		_, err = proveReachableOffHost(t.Context(), mailbox, []string{"127.0.0.1", "192.0.2.1"})
		if err == nil {
			t.Fatal("an unreachable mailbox was reported as reachable")
		}
		for _, want := range []string{
			"no other address of this host", "--supervisor-address", "127.0.0.1", "192.0.2.1",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})
}

func TestRefuseNoOffHostCandidate(t *testing.T) {
	t.Run("a v4 bind on a host holding only v6 names the rebind flag", func(t *testing.T) {
		held := ips(t, "2001:db8::5")
		for transport, want := range map[mailboxTransport]string{
			transportSSH:   "--listen [::]:7422",
			transportHTTPS: "--host ::",
		} {
			record := mailboxRecord{Transport: transport, Listen: "0.0.0.0:7422"}
			err := refuseNoOffHostCandidate(record, 7422, held)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: err = %v, want it to name %q", transport, err, want)
			}
		}
	})

	t.Run("a host with nothing outside loopback says so", func(t *testing.T) {
		record := mailboxRecord{Transport: transportSSH, Listen: ":7422"}
		err := refuseNoOffHostCandidate(record, 7422, ips(t, "127.0.0.1", "::1"))
		if err == nil || !strings.Contains(err.Error(), "no address outside loopback") {
			t.Fatalf("err = %v, want the no-network branch", err)
		}
		if !strings.Contains(err.Error(), "--supervisor-address") {
			t.Fatalf("err = %v, want the escape hatch", err)
		}
	})
}

func TestSupervisorCandidates(t *testing.T) {
	// A recorded listen address that is not [host]:port yields no offer rather
	// than a panic or a bogus endpoint: detectMailbox already refuses it, and the
	// preflight renders the refusal instead of a picker.
	t.Run("an unparseable listen address offers nothing", func(t *testing.T) {
		mailbox := detectedMailbox{Transport: transportHTTPS, Listen: "9020", Port: 9020}
		if got := supervisorCandidates(mailbox); got != nil {
			t.Fatalf("candidates = %v, want none", got)
		}
	})

	// A bind pinned to one address answers there and nowhere else, so the offer
	// collapses to it — and it is rendered as the endpoint the field takes, not
	// as a bare address the operator would have to wrap by hand.
	t.Run("a pinned bind is offered as one endpoint of the right transport", func(t *testing.T) {
		for transport, want := range map[mailboxTransport]string{
			transportHTTPS: "https://192.168.1.20:9020",
			transportSSH:   "ssh://192.168.1.20:9020",
		} {
			mailbox := detectedMailbox{Transport: transport, Listen: "192.168.1.20:9020", Port: 9020}
			if got := supervisorCandidates(mailbox); !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("%s candidates = %v, want [%s]", transport, got, want)
			}
		}
	})
}

func TestMailboxEndpoints(t *testing.T) {
	mailbox := detectedMailbox{Transport: transportHTTPS, Port: 9020}
	// IPv6 must keep its brackets, or the port reads as part of the address, and
	// the ranking the caller computed must survive rendering.
	got := mailboxEndpoints(mailbox, []string{"192.168.1.20", "2001:db8::5"})
	want := []string{"https://192.168.1.20:9020", "https://[2001:db8::5]:9020"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("endpoints = %v, want %v", got, want)
	}
}

func TestMailboxEndpointList(t *testing.T) {
	// The Kubernetes refusal interpolates this mid-sentence, so an empty proof
	// has to read as prose rather than leaving a dangling "answers on ,".
	if got := mailboxEndpointList(detectedMailbox{Transport: transportSSH, Port: 7422}); got != "no address other than loopback" {
		t.Fatalf("empty list = %q", got)
	}
	mailbox := detectedMailbox{
		Transport: transportHTTPS, Port: 9020,
		OffHostAddresses: []string{"192.168.1.20", "2001:db8::5"},
	}
	// IPv6 must keep its brackets, or the port reads as part of the address.
	if got, want := mailboxEndpointList(mailbox), "https://192.168.1.20:9020, https://[2001:db8::5]:9020"; got != want {
		t.Fatalf("list = %q, want %q", got, want)
	}
}
