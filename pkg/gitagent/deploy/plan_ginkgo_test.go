package deploy_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// defaultSizing mirrors the command's flag defaults.
func defaultSizingRequest() deploy.SizingRequest {
	return deploy.SizingRequest{
		CPURequest:    "500m",
		CPULimit:      "2",
		MemoryRequest: "1Gi",
		MemoryLimit:   "4Gi",
		Storage:       "20Gi",
		TmpSize:       "1Gi",
		PidsLimit:     1024,
	}
}

var _ = Describe("ParseTarget", func() {
	It("accepts the two runtimes and normalizes case", func() {
		Expect(deploy.ParseTarget("docker")).To(Equal(deploy.TargetDocker))
		Expect(deploy.ParseTarget("  KUBERNETES ")).To(Equal(deploy.TargetKubernetes))
	})

	It("refuses an empty target rather than defaulting", func() {
		_, err := deploy.ParseTarget("")
		Expect(err).To(MatchError(ContainSubstring("--target is required")))
	})

	It("names the valid set when the target is unknown", func() {
		_, err := deploy.ParseTarget("podman")
		Expect(err).To(MatchError(ContainSubstring("docker, kubernetes")))
	})
})

var _ = Describe("ParseSizing", func() {
	It("converts k8s quantities into the units docker's flags take", func() {
		sizing, err := deploy.ParseSizing(defaultSizingRequest())
		Expect(err).NotTo(HaveOccurred())

		Expect(sizing.DockerCPUs()).To(Equal("2"))
		Expect(sizing.DockerMemoryBytes()).To(Equal("4294967296"))            // 4Gi
		Expect(sizing.DockerMemoryReservationBytes()).To(Equal("1073741824")) // 1Gi
		Expect(sizing.DockerTmpfsSize()).To(Equal("1073741824"))
	})

	It("renders a fractional CPU limit", func() {
		request := defaultSizingRequest()
		request.CPULimit = "500m"
		request.CPURequest = "250m"

		sizing, err := deploy.ParseSizing(request)
		Expect(err).NotTo(HaveOccurred())
		Expect(sizing.DockerCPUs()).To(Equal("0.5"))
	})

	DescribeTable("refuses a malformed quantity, naming the flag",
		func(mutate func(*deploy.SizingRequest), wantSubstring string) {
			request := defaultSizingRequest()
			mutate(&request)
			_, err := deploy.ParseSizing(request)
			Expect(err).To(MatchError(ContainSubstring(wantSubstring)))
		},
		// "4GB" is not a k8s quantity; a hand-rolled parser would accept it.
		Entry("GB is not a suffix", func(r *deploy.SizingRequest) { r.MemoryLimit = "4GB" }, "--memory-limit"),
		Entry("not a number", func(r *deploy.SizingRequest) { r.CPULimit = "lots" }, "--cpu-limit"),
		Entry("zero storage", func(r *deploy.SizingRequest) { r.Storage = "0" }, "greater than zero"),
		Entry("negative pids", func(r *deploy.SizingRequest) { r.PidsLimit = -1 }, "--pids-limit"),
	)

	It("refuses a limit below its own request", func() {
		request := defaultSizingRequest()
		request.MemoryLimit = "512Mi"
		_, err := deploy.ParseSizing(request)
		Expect(err).To(MatchError(ContainSubstring("--memory-limit 512Mi is below --memory-request 1Gi")))
	})
})

var _ = Describe("Plan", func() {
	plan := deploy.Plan{
		Name:            "worker-01",
		Backend:         "git-agent",
		Target:          deploy.TargetDocker,
		Home:            "/home/claude",
		ListenPort:      7422,
		HostPort:        7423,
		Supervisor:      "ssh://captain@host.docker.internal:7422",
		Advertise:       "ssh://captain@127.0.0.1:7423/repo.git",
		HostFingerprint: "SHA256:abc",
		JoinPath:        "/run/captain/join",
	}

	It("derives workload names that are valid DNS labels and object names", func() {
		Expect(plan.WorkloadName()).To(Equal("captain-git-agent-worker-01"))
		Expect(plan.VolumeName()).To(Equal("captain-git-agent-worker-01-state"))
		Expect(plan.JoinSecretName()).To(Equal("captain-git-agent-worker-01-join"))
	})

	It("labels every object so teardown can find it by selector", func() {
		Expect(plan.Labels()).To(Equal(map[string]string{
			"app.kubernetes.io/name":          "captain-git-agent",
			"app.kubernetes.io/instance":      "worker-01",
			"app.kubernetes.io/managed-by":    "captain",
			"captain.flanksource.com/backend": "git-agent",
		}))
	})

	// Both addresses must be explicit. Omitted, the receiver derives the agent's
	// address from the connection source — a pod IP or a Docker Desktop VM
	// address the supervisor cannot route to — and the agent enrols anyway, so
	// the failure appears only at the first dispatch.
	It("passes both addresses and the token file, and never the token", func() {
		Expect(plan.ServeArgs()).To(Equal([]string{
			"sandbox", "git-agent", "serve",
			"--role", "sidecar",
			"--transport", "ssh",
			"--backend", "git-agent",
			"--listen", "0.0.0.0:7422",
			"--advertise", "ssh://captain@127.0.0.1:7423/repo.git",
			"--supervisor", "ssh://captain@host.docker.internal:7422",
			"--host-fingerprint", "SHA256:abc",
			"--token-file", "/run/captain/join",
		}))
		// argv is visible in `docker inspect`, in a pod spec, and in
		// /proc/<pid>/cmdline, so the credential itself must never appear there.
		Expect(plan.ServeArgs()).NotTo(ContainElement("--token"))
	})

	It("starts persisted enrollment without asking for the spent join token", func() {
		restart := plan
		restart.JoinPath = ""

		Expect(restart.ServeArgs()).NotTo(ContainElement("--token-file"))
		Expect(restart.ServeArgs()).NotTo(ContainElement("/run/captain/join"))
	})

	// The workload has to serve the protocol the supervisor was told to dispatch
	// to; serving the other one accepts the connection and fails the handshake.
	It("renders the transport its advertise URL implies", func() {
		plan.Transport = "https"
		plan.Advertise = "https://w1.example.com/git/repo.git"
		Expect(plan.ServeArgs()).To(ContainElements("--transport", "https"))
		// Still never the credential, whichever transport carries it.
		Expect(plan.ServeArgs()).NotTo(ContainElement("--token"))
		Expect(strings.Join(plan.ServeArgs(), " ")).NotTo(ContainSubstring("cptn_"))
	})
})
