// Which of this host's addresses a workload in another network namespace can
// reach the mailbox on.
//
// The routing table names exactly one — the source address a packet to the
// internet would carry — and on a laptop behind a VPN that is a tunnel address
// no container can reach, while the LAN address and the docker bridge gateway
// both work. It is also not the address the workload uses: a docker sidecar
// dials host.docker.internal, which resolves to the bridge gateway on Linux and
// to a VM-internal address on Docker Desktop. So this probes every address the
// host holds and reports all that answered, because the honest claim is "the
// mailbox answers off loopback", not "it answers on the one address that
// matters".
package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/gitagent"
)

// proveOffHostReach returns every address of this host that answered as this
// mailbox, best-ranked first.
func proveOffHostReach(ctx context.Context, record mailboxRecord, mailbox detectedMailbox) ([]string, error) {
	held, err := hostInterfaceIPs()
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(record.Listen)
	if err != nil {
		return nil, fmt.Errorf("recorded mailbox listen address %q is not [host]:port: %w", record.Listen, err)
	}
	candidates := offHostCandidates(host, primaryOutboundAddress(), held)
	if len(candidates) == 0 {
		return nil, refuseNoOffHostCandidate(record, mailbox.Port, held)
	}
	return proveReachableOffHost(ctx, mailbox, candidates)
}

// offHostCandidates ranks the addresses worth probing, best first. It is pure so
// the ranking can be tested without a network: primary is the routing table's
// answer or "", held is every unicast address this host carries.
func offHostCandidates(bindHost, primary string, held []net.IP) []string {
	bindHost = strings.TrimSpace(bindHost)
	// A listener bound to one address answers there and nowhere else, so every
	// other candidate would be a guaranteed failure line in the refusal. A
	// hostname parses as no IP and falls through to enumeration, which is right:
	// we cannot know which address it resolved to.
	if ip := net.ParseIP(bindHost); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
		return []string{ip.String()}
	}
	// An IPv4 wildcard socket can never accept an IPv6 connection. "" and "::"
	// keep both families, because Go's wildcard listener is dual-stack.
	onlyV4 := bindHost == "0.0.0.0"

	ranked := make([]string, 0, len(held)+1)
	seen := map[string]bool{}
	add := func(ip net.IP) {
		if onlyV4 && ip.To4() == nil {
			return
		}
		if text := ip.String(); !seen[text] {
			seen[text] = true
			ranked = append(ranked, text)
		}
	}
	// The routing table's answer first: it is the best single guess, and being
	// wrong about it is no longer fatal now that the rest follow.
	if ip := net.ParseIP(strings.TrimSpace(primary)); isUsableHostIP(ip) {
		add(ip)
	}
	usable := usableHostIPs(held)
	sortHostIPs(usable)
	for _, ip := range usable {
		add(ip)
	}
	return ranked
}

// supervisorCandidates is every address of this host an operator could hand a
// workload as the supervisor endpoint, best-ranked first.
//
// It deliberately probes nothing, which is what separates it from
// proveOffHostReach above. The caller is the kubernetes path, where the workload
// runs in a cluster this host is not in: a probe from here proves the mailbox
// answers on an address, never that the cluster can route to it. Paying up to
// the transport's probe timeout for a claim that does not transfer would spend
// the web preflight's whole budget to narrow the list on the wrong axis.
//
// An address this cannot resolve yields no candidates rather than an error: the
// list is an offer, and detectMailbox already refuses a mailbox that is unusable.
func supervisorCandidates(mailbox detectedMailbox) []string {
	held, err := hostInterfaceIPs()
	if err != nil {
		return nil
	}
	host, _, err := net.SplitHostPort(mailbox.Listen)
	if err != nil {
		return nil
	}
	return mailboxEndpoints(mailbox, offHostCandidates(host, primaryOutboundAddress(), held))
}

// isUsableHostIP reports whether an address could carry a connection from
// another network namespace.
//
// Link-local is excluded because InterfaceAddrs returns fe80:: without the zone
// index a dial would need, and 169.254/16 is not routed off the link either.
// IsPrivate is deliberately NOT a filter: a publicly addressed host is a
// legitimate supervisor.
func isUsableHostIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

func usableHostIPs(held []net.IP) []net.IP {
	usable := make([]net.IP, 0, len(held))
	for _, ip := range held {
		if isUsableHostIP(ip) {
			usable = append(usable, ip)
		}
	}
	return usable
}

// sortHostIPs orders IPv4 before IPv6 and then bytewise.
//
// For stability, not preference: net.InterfaceAddrs returns kernel order, and a
// refusal that lists every candidate in a different order on each run cannot be
// diffed against the last one.
func sortHostIPs(ips []net.IP) {
	sort.Slice(ips, func(i, j int) bool {
		left, right := ips[i].To4(), ips[j].To4()
		if (left != nil) != (right != nil) {
			return left != nil
		}
		return bytes.Compare(ips[i].To16(), ips[j].To16()) < 0
	})
}

// hostInterfaceIPs is every unicast address this host holds.
func hostInterfaceIPs() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf(
			"could not enumerate this host's addresses, so no route to the mailbox can be proven; "+
				"pass --supervisor-address: %w", err)
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if network, ok := addr.(*net.IPNet); ok && network.IP != nil {
			ips = append(ips, network.IP)
		}
	}
	return ips, nil
}

// primaryOutboundAddress returns the routing table's source address for an
// off-host packet, or "" when it has none.
//
// The UDP "dial" transmits nothing — it only asks which source address a packet
// to that destination would carry. An empty answer is not an error: a host with
// no default route still holds a docker bridge a container reaches it on, and
// refusing here is the offline-laptop failure this ranking exists to remove.
func primaryOutboundAddress() string {
	conn, err := net.Dial("udp", "203.0.113.1:9") // TEST-NET-3, never routed
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return addr.IP.String()
}

// proveReachableOffHost probes every candidate at once and returns those that
// answered as this mailbox, in the order given.
//
// Concurrent rather than serial because each probe costs up to the transport's
// 5s probe timeout while the web preflight budgets 8s for the whole detection,
// so sweeping a laptop's interfaces one at a time would report "the mailbox
// probe did not finish" instead of an answer. Every result is collected rather
// than cancelling siblings on the first success, because the addresses that did
// NOT answer are exactly what the refusal has to name.
//
// The candidate list is a parameter rather than something this derives, so a
// test can drive it with addresses a sandboxed host can actually serve.
func proveReachableOffHost(ctx context.Context, mailbox detectedMailbox, candidates []string) ([]string, error) {
	failures := make([]error, len(candidates))
	var wait sync.WaitGroup
	for i, candidate := range candidates {
		wait.Add(1)
		go func() {
			defer wait.Done()
			failures[i] = gitagent.VerifyEndpointIdentity(ctx,
				mailboxEndpoint(mailbox.Transport, candidate, mailbox.Port), mailbox.HostFingerprint)
		}()
	}
	wait.Wait()

	reachable := make([]string, 0, len(candidates))
	var refused strings.Builder
	for i, candidate := range candidates {
		if failures[i] == nil {
			reachable = append(reachable, candidate)
			continue
		}
		// The address is repeated even though the probe error already carries
		// host:port, so a single grepped line still names what it is about.
		fmt.Fprintf(&refused, "\n  %s: %v", candidate, failures[i])
	}
	if len(reachable) > 0 {
		return reachable, nil
	}
	return nil, fmt.Errorf(
		"the mailbox answers on %s but on no other address of this host, so a workload in another network "+
			"namespace cannot relay to it; a host firewall that accepts on loopback and drops the rest is the "+
			"usual cause. Pass --supervisor-address with an address the workload can reach to enroll without "+
			"this proof:%s",
		mailboxEndpoint(mailbox.Transport, "127.0.0.1", mailbox.Port), refused.String())
}

// refuseNoOffHostCandidate explains an empty candidate list, which has two
// causes and two different fixes.
func refuseNoOffHostCandidate(record mailboxRecord, port int, held []net.IP) error {
	if len(usableHostIPs(held)) == 0 {
		return fmt.Errorf(
			"this host holds no address outside loopback, so nothing in another network namespace could reach "+
				"the mailbox on %s; connect a network, or pass --supervisor-address with an address the "+
				"workload can reach", record.Listen)
	}
	// Everything this host holds was dropped by the family filter, so the bind
	// is the thing to change. Names the flag the recording process takes, the
	// way refuseUnusableMailbox already does.
	rebind := fmt.Sprintf("--listen [::]:%d", port)
	if record.Transport == transportHTTPS {
		rebind = "--host ::"
	}
	return fmt.Errorf(
		"the mailbox is bound to %s, an IPv4 socket, and this host holds no off-loopback IPv4 address; "+
			"restart it with %s so it answers on both families, or pass --supervisor-address with an address "+
			"the workload can reach", record.Listen, rebind)
}

// mailboxEndpoint is the URL a client dials this mailbox on at one address.
func mailboxEndpoint(transport mailboxTransport, address string, port int) string {
	return fmt.Sprintf("%s://%s", transport, net.JoinHostPort(address, strconv.Itoa(port)))
}

// mailboxEndpoints renders addresses as endpoints of one mailbox, preserving
// their order.
func mailboxEndpoints(mailbox detectedMailbox, addresses []string) []string {
	endpoints := make([]string, 0, len(addresses))
	for _, address := range addresses {
		endpoints = append(endpoints, mailboxEndpoint(mailbox.Transport, address, mailbox.Port))
	}
	return endpoints
}

// mailboxEndpointList renders every proven address as an endpoint, for the
// Kubernetes refusal that has no other context to hang a port on.
func mailboxEndpointList(mailbox detectedMailbox) string {
	endpoints := mailboxEndpoints(mailbox, mailbox.OffHostAddresses)
	if len(endpoints) == 0 {
		return "no address other than loopback"
	}
	return strings.Join(endpoints, ", ")
}
