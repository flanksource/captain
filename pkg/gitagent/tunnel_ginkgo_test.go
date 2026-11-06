package gitagent_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

const tunnelTask = "t-0123456789ab"

// startEchoService answers each line with "echo: <line>", standing in for a
// loopback service the supervisor run publishes.
func startEchoService() string {
	GinkgoHelper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(listener.Close)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					fmt.Fprintf(conn, "echo: %s\n", scanner.Text())
				}
			}()
		}
	}()
	return listener.Addr().String()
}

// roundTrip sends one line through a tunnel and returns the reply line.
func roundTrip(conn net.Conn, line string) (string, error) {
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, line); err != nil {
		return "", err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	return strings.TrimSpace(reply), err
}

// tunnelWorld is a mailbox repo holding one task dispatched to worker-1, with
// an echo service registered for it.
type tunnelWorld struct {
	root, route, repo string
}

func newTunnelWorld(route string) tunnelWorld {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	repo := filepath.Join(root, filepath.FromSlash(route))
	Expect(os.MkdirAll(repo, 0o755)).To(Succeed())
	Expect(gitagent.SaveTaskState(repo, &gitagent.TaskState{Task: tunnelTask, Agent: "worker-1"})).To(Succeed())
	Expect(gitagent.WriteServices(repo, tunnelTask, gitagent.Services{"tools": startEchoService()})).To(Succeed())
	return tunnelWorld{root: root, route: route, repo: repo}
}

var _ = Describe("Service tunnel", func() {
	DescribeTable("refuses a registry entry that is not a loopback service",
		func(services gitagent.Services, message string) {
			Expect(services.Validate()).To(MatchError(ContainSubstring(message)))
		},
		Entry("a remote host", gitagent.Services{"tools": "10.0.0.5:80"}, "not a loopback IP"),
		Entry("a host name", gitagent.Services{"tools": "localhost:80"}, "not a loopback IP"),
		Entry("no port", gitagent.Services{"tools": "127.0.0.1"}, "not host:port"),
		Entry("a path for a name", gitagent.Services{"../tools": "127.0.0.1:80"}, "not a valid service name"),
	)

	Context("in the serve process", func() {
		var world tunnelWorld
		BeforeEach(func() { world = newTunnelWorld("mailbox.git") })

		It("splices the dispatched agent to its task's service", func() {
			conn, err := gitagent.OpenService(world.repo, "worker-1", tunnelTask, "tools")
			Expect(err).NotTo(HaveOccurred())
			Expect(roundTrip(conn, "ping")).To(Equal("echo: ping"))
		})

		DescribeTable("refuses",
			func(agent, task, service, message string) {
				_, err := gitagent.OpenService(world.repo, agent, task, service)
				Expect(err).To(MatchError(ContainSubstring(message)))
			},
			Entry("another agent", "worker-2", tunnelTask, "tools", `not dispatched to agent "worker-2"`),
			Entry("an unknown task", "worker-1", "t-ffffffffffff", "tools", "unknown"),
			Entry("an unregistered service", "worker-1", tunnelTask, "egress/lookup", `no service "egress/lookup"`),
		)

		It("refuses every service once they are withdrawn", func() {
			Expect(gitagent.RemoveServices(world.repo, tunnelTask)).To(Succeed())
			_, err := gitagent.OpenService(world.repo, "worker-1", tunnelTask, "tools")
			Expect(err).To(MatchError(ContainSubstring("publishes no services")))
		})

		It("keeps the registry private to the supervisor's user", func() {
			info, err := os.Stat(filepath.Join(world.repo, "captain", "tasks", tunnelTask, "services.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		})
	})

	Context("over ssh", func() {
		It("reaches the service with the enrolled key and pinned host key, and is refused for another agent's task", func() {
			world := newTunnelWorld("mailbox.git")
			_, agentFP, agentKey := newClientKey()
			_, strangerFP, strangerKey := newClientKey()
			directory := &memoryDirectory{agents: map[string]string{agentFP: "worker-1", strangerFP: "worker-2"}, pending: map[string]string{}}
			addr, hostFP := startTestServer(directory, world.root, gitagent.RoleMailbox)
			target := func(key string) gitagent.TransportTarget {
				return gitagent.TransportTarget{URL: "ssh://captain@" + addr + "/" + world.route, KeyPath: key, HostFingerprint: hostFP}
			}

			conn, err := gitagent.DialService(context.Background(), target(agentKey), tunnelTask, "tools")
			Expect(err).NotTo(HaveOccurred())
			Expect(roundTrip(conn, "over ssh")).To(Equal("echo: over ssh"))

			conn, err = gitagent.DialService(context.Background(), target(strangerKey), tunnelTask, "tools")
			Expect(err).NotTo(HaveOccurred(), "the refusal arrives with the first read")
			_, err = roundTrip(conn, "intrusion")
			Expect(err).To(MatchError(ContainSubstring(`not dispatched to agent "worker-2"`)))
		})

		It("relays local connections through a forwarder", func() {
			world := newTunnelWorld("mailbox.git")
			_, agentFP, agentKey := newClientKey()
			directory := &memoryDirectory{agents: map[string]string{agentFP: "worker-1"}, pending: map[string]string{}}
			addr, hostFP := startTestServer(directory, world.root, gitagent.RoleMailbox)
			target := gitagent.TransportTarget{URL: "ssh://captain@" + addr + "/" + world.route, KeyPath: agentKey, HostFingerprint: hostFP}

			local, closeForwarder, err := gitagent.ServeForwarder(context.Background(), gitagent.Dialer(target, tunnelTask), "tools")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(closeForwarder)
			for _, line := range []string{"first", "second"} {
				conn, err := net.Dial("tcp", local)
				Expect(err).NotTo(HaveOccurred())
				Expect(roundTrip(conn, line)).To(Equal("echo: " + line))
			}
		})
	})

	Context("over https", func() {
		It("reaches the service through an upgraded request, and refuses an unregistered one before upgrading", func() {
			fixture := newGitHTTPFixture()
			fixture.agent = "worker-1"
			repo := filepath.Join(fixture.root, filepath.FromSlash(fixture.repoRoute))
			Expect(gitagent.SaveTaskState(repo, &gitagent.TaskState{Task: tunnelTask, Agent: "worker-1"})).To(Succeed())
			Expect(gitagent.WriteServices(repo, tunnelTask, gitagent.Services{"tools": startEchoService()})).To(Succeed())
			mailboxURL, err := gitagent.HTTPSRepoURL(fixture.endpoint(), fixture.repoRoute)
			Expect(err).NotTo(HaveOccurred())
			target := gitagent.TransportTarget{
				URL: mailboxURL, Token: text.NewSensitiveString("cptn_id.secret"),
				CAPath: fixture.credential.CertPath, PinnedPublicKey: fixture.credential.PublicKeyPin,
			}

			conn, err := gitagent.DialService(context.Background(), target, tunnelTask, "tools")
			Expect(err).NotTo(HaveOccurred())
			Expect(roundTrip(conn, "over https")).To(Equal("echo: over https"))

			_, err = gitagent.DialService(context.Background(), target, tunnelTask, "creds/lookup")
			Expect(err).To(MatchError(And(ContainSubstring("403"), ContainSubstring(`no service "creds/lookup"`))))
		})

		It("refuses a plain request that does not ask to upgrade", func() {
			fixture := newGitHTTPFixture()
			resp, err := fixture.client().Get(fixture.endpoint() + gitagent.GitHTTPPrefix + fixture.repoRoute + "/" + gitagent.TunnelCommand + "?task=" + tunnelTask + "&service=tools")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			Expect(resp.StatusCode).To(Equal(http.StatusUpgradeRequired))
		})
	})
})
