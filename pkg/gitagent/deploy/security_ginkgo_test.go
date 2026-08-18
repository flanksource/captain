package deploy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
	"github.com/flanksource/captain/pkg/sandbox"
)

const workloadHome = "/home/claude"

var _ = Describe("HardenedSecurity", func() {
	It("runs unprivileged with no capabilities and a read-only root", func() {
		hardened := deploy.HardenedSecurity()
		Expect(hardened.RunAsUser).NotTo(BeZero())
		Expect(hardened.CapAdd).To(BeEmpty())
		Expect(hardened.ReadOnlyRoot).To(BeTrue())
	})

	It("describes the posture for the operator", func() {
		Expect(deploy.HardenedSecurity().Describe()).To(SatisfyAll(
			ContainSubstring("uid=501:20"),
			ContainSubstring("caps=none"),
			ContainSubstring("no-new-privileges"),
			ContainSubstring("read-only-root"),
		))
	})
})

var _ = Describe("RefuseUnsafe", func() {
	It("accepts the hardened default", func() {
		Expect(deploy.RefuseUnsafe(deploy.HardenedSecurity(), workloadHome, nil, nil)).To(Succeed())
	})

	// A process that can reach the daemon can start a privileged container
	// bind-mounting the host root, so this is a full escape (R5.3, A6.2). The
	// list is shared with the SRT adapter, so this table cannot drift from it.
	It("refuses every container-runtime socket the deny list names", func() {
		sockets := sandbox.ContainerRuntimeSockets(workloadHome)
		Expect(sockets).NotTo(BeEmpty())

		for _, socket := range sockets {
			mount := socket + ":" + socket
			err := deploy.RefuseUnsafe(deploy.HardenedSecurity(), workloadHome, []string{mount}, nil)
			Expect(err).To(MatchError(ContainSubstring("full host escape")), "socket %s was allowed", socket)
		}
	})

	It("refuses a socket mount written with a non-canonical path", func() {
		err := deploy.RefuseUnsafe(deploy.HardenedSecurity(), workloadHome,
			[]string{"/var/run/../run/docker.sock:/var/run/docker.sock"}, nil)
		Expect(err).To(MatchError(ContainSubstring("full host escape")))
	})

	It("allows an ordinary mount", func() {
		Expect(deploy.RefuseUnsafe(deploy.HardenedSecurity(), workloadHome,
			[]string{"/srv/cache:/cache:ro"}, nil)).To(Succeed())
	})

	// These presets expand into a real socket bind mount, so selecting one is
	// the same escape by another name.
	DescribeTable("refuses a preset that grants the runtime socket",
		func(preset string) {
			err := deploy.RefuseUnsafe(deploy.HardenedSecurity(), workloadHome, nil, []string{preset})
			Expect(err).To(MatchError(ContainSubstring("container runtime socket")))
		},
		Entry("claude", "claude"),
		Entry("docker", "docker"),
		Entry("case-insensitively", "Docker"),
	)

	It("allows presets that grant no socket", func() {
		Expect(deploy.RefuseUnsafe(deploy.HardenedSecurity(), workloadHome, nil,
			[]string{"golang", "git"})).To(Succeed())
	})

	It("refuses running agent-authored code as root", func() {
		asRoot := deploy.HardenedSecurity()
		asRoot.RunAsUser = 0
		Expect(deploy.RefuseUnsafe(asRoot, workloadHome, nil, nil)).
			To(MatchError(ContainSubstring("--run-as-user 0")))
	})

	DescribeTable("refuses a network mode that dissolves the boundary",
		func(network, wantSubstring string) {
			security := deploy.HardenedSecurity()
			security.Network = network
			Expect(deploy.RefuseUnsafe(security, workloadHome, nil, nil)).
				To(MatchError(ContainSubstring(wantSubstring)))
		},
		Entry("host exposes loopback services", "host", "host network namespace"),
		Entry("none breaks dispatch and relay", "none", "dispatch, relay"),
	)
})
