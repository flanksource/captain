package deploy_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

const joinHostPath = "/home/op/.captain/sandbox/deploy/worker-01/join"

func dockerPlan() deploy.Plan {
	sizing, err := deploy.ParseSizing(defaultSizingRequest())
	Expect(err).NotTo(HaveOccurred())
	return deploy.Plan{
		Name:            "worker-01",
		Backend:         "git-agent",
		Target:          deploy.TargetDocker,
		Image:           "ghcr.io/flanksource/captain:latest",
		Home:            "/home/claude",
		ListenPort:      7422,
		HostPort:        7423,
		Supervisor:      "ssh://captain@host.docker.internal:7422",
		Advertise:       "ssh://captain@127.0.0.1:7423/repo.git",
		HostFingerprint: "SHA256:abc",
		JoinPath:        deploy.JoinMountPath,
		Sizing:          sizing,
		Security:        deploy.HardenedSecurity(),
	}
}

// hasFlagValue reports whether argv contains `flag value` adjacently.
func hasFlagValue(argv []string, flag, value string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

var _ = Describe("DockerArgs", func() {
	var argv []string

	BeforeEach(func() { argv = deploy.DockerArgs(dockerPlan(), joinHostPath) })

	// Each of these is the containment boundary for agent-authored code. A
	// silent regression in any one of them is the failure this package exists
	// to prevent, so they are asserted individually rather than as a blob.
	DescribeTable("applies the hardened posture",
		func(flag, value string) {
			Expect(hasFlagValue(argv, flag, value)).To(BeTrue(), "missing %s %s in %v", flag, value, argv)
		},
		Entry("drops every capability", "--cap-drop", "ALL"),
		Entry("forbids privilege escalation", "--security-opt", "no-new-privileges"),
		Entry("runs as the image's unprivileged user", "--user", "501:20"),
		// gosu would need CAP_SETUID/CAP_SETGID, which --cap-drop ALL removes.
		Entry("bypasses the gosu entrypoint", "--entrypoint", "captain"),
		Entry("caps CPU", "--cpus", "2"),
		Entry("caps memory", "--memory", "4294967296"),
		Entry("denies swap past the memory cap", "--memory-swap", "4294967296"),
		Entry("reserves memory", "--memory-reservation", "1073741824"),
		Entry("bounds processes", "--pids-limit", "1024"),
		Entry("reaches the mailbox via the gateway alias", "--add-host", "host.docker.internal:host-gateway"),
		Entry("publishes only on loopback", "--publish", "127.0.0.1:7423:7422"),
		Entry("mounts state at the image's own home", "--volume", "captain-git-agent-worker-01-state:/home/claude"),
		Entry("mounts the token read-only", "--volume", joinHostPath+":/run/captain/join:ro"),
		Entry("steers scratch off the tmpfs", "--env", "TMPDIR=/home/claude/.cache/tmp"),
	)

	// The coding agent is launched with Setsid, so its children reparent to
	// PID 1. captain is not a reaper, so without --init zombies accumulate
	// against --pids-limit until forks start failing.
	It("reaps orphaned children", func() {
		Expect(argv).To(ContainElement("--init"))
	})

	It("mounts a read-only root with a sized tmpfs for scratch", func() {
		Expect(argv).To(ContainElement("--read-only"))
		Expect(hasFlagValue(argv, "--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=1073741824")).To(BeTrue())
	})

	It("names the workload and labels it for teardown by selector", func() {
		Expect(hasFlagValue(argv, "--name", "captain-git-agent-worker-01")).To(BeTrue())
		Expect(hasFlagValue(argv, "--label", "app.kubernetes.io/instance=worker-01")).To(BeTrue())
		Expect(hasFlagValue(argv, "--label", "captain.flanksource.com/backend=git-agent")).To(BeTrue())
	})

	It("ends with the image followed by the serve argv", func() {
		image := indexOf(argv, "ghcr.io/flanksource/captain:latest")
		Expect(image).To(BeNumerically(">", 0))
		Expect(argv[image+1:]).To(Equal(dockerPlan().ServeArgs()))
	})

	// R5.3/A6.2: a runtime socket is a full host escape, and there is no flag
	// that can add one.
	It("never mounts a container runtime socket and is never privileged", func() {
		joined := strings.Join(argv, " ")
		Expect(joined).NotTo(ContainSubstring("docker.sock"))
		Expect(joined).NotTo(ContainSubstring("containerd.sock"))
		Expect(joined).NotTo(ContainSubstring("podman.sock"))
		Expect(argv).NotTo(ContainElement("--privileged"))
		Expect(hasFlagValue(argv, "--network", "host")).To(BeFalse())
	})

	// R8.2: the token reaches the workload as a file. In argv it would show up
	// in `docker inspect` and /proc/1/cmdline, readable by the coding agent this
	// very container runs.
	It("keeps the join token out of argv entirely", func() {
		const token = "s3cret-join-token"
		plan := dockerPlan()
		for _, arg := range deploy.DockerArgs(plan, joinHostPath) {
			Expect(arg).NotTo(ContainSubstring(token))
		}
		Expect(deploy.DockerArgs(plan, joinHostPath)).NotTo(ContainElement("--join"))
	})

	It("does not mount a spent join token when restarting persisted enrollment", func() {
		plan := dockerPlan()
		plan.JoinPath = ""
		argv := deploy.DockerArgs(plan, "")

		Expect(argv).To(ContainElement("--volume"), "the state volume must still be mounted")
		Expect(argv).NotTo(ContainElement("--token-file"))
		for _, arg := range argv {
			Expect(arg).NotTo(HaveSuffix(":" + deploy.JoinMountPath + ":ro"))
		}
	})

	It("forwards environment by name only, so values stay out of argv", func() {
		plan := dockerPlan()
		plan.EnvNames = []string{"ANTHROPIC_API_KEY"}

		argv := deploy.DockerArgs(plan, joinHostPath)
		Expect(hasFlagValue(argv, "--env", "ANTHROPIC_API_KEY")).To(BeTrue())
		for _, arg := range argv {
			Expect(arg).NotTo(HavePrefix("ANTHROPIC_API_KEY="))
		}
	})

	It("omits the read-only root and its tmpfs when disabled", func() {
		plan := dockerPlan()
		plan.Security.ReadOnlyRoot = false

		argv := deploy.DockerArgs(plan, joinHostPath)
		Expect(argv).NotTo(ContainElement("--read-only"))
		Expect(argv).NotTo(ContainElement("--tmpfs"))
	})

	It("adds back only the capabilities explicitly requested", func() {
		plan := dockerPlan()
		plan.Security.CapAdd = []string{"NET_ADMIN"}

		argv := deploy.DockerArgs(plan, joinHostPath)
		Expect(hasFlagValue(argv, "--cap-drop", "ALL")).To(BeTrue())
		Expect(hasFlagValue(argv, "--cap-add", "NET_ADMIN")).To(BeTrue())
	})
})

func indexOf(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}
