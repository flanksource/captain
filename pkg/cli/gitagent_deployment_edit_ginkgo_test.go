package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

var _ = Describe("editable git-agent deployments", func() {
	DescribeTable("projects the resolved workload settings without secret values", func(
		target deploy.Target,
		namespace string,
		mutate func(*GitAgentDeployOptions, *deploy.Plan),
		assert func(GitAgentDeploymentConfig),
	) {
		opts := deployOptions("worker-01")
		opts.DryRun = false
		opts.Target = string(target)
		plan := deploy.Plan{
			Name: "worker-01", Backend: opts.Backend, Target: target,
			Image: opts.Image, Home: opts.Home, ListenPort: opts.ListenPort,
			Supervisor: opts.SupervisorAddress, Advertise: opts.Advertise,
		}
		mutate(&opts, &plan)

		config := deploymentConfig(plan, opts, namespace)

		Expect(config.Target).To(Equal(string(target)))
		Expect(config.Transport).To(Equal(opts.Transport))
		Expect(config.Image).To(Equal(opts.Image))
		Expect(config.Namespace).To(Equal(namespace))
		Expect(config.SupervisorAddress).To(Equal(plan.Supervisor))
		Expect(config.Advertise).To(Equal(plan.Advertise))
		Expect(config.CPULimit).To(Equal(opts.CPULimit))
		Expect(config.MemoryLimit).To(Equal(opts.MemoryLimit))
		Expect(config.Storage).To(Equal(opts.Storage))
		Expect(config.ReadOnlyRoot).NotTo(BeNil())
		Expect(*config.ReadOnlyRoot).To(Equal(opts.ReadOnlyRoot))
		Expect(config.Wait).NotTo(BeNil())
		Expect(*config.Wait).To(Equal(opts.Wait))
		assert(config)
	},
		Entry("docker", deploy.TargetDocker, "", func(opts *GitAgentDeployOptions, plan *deploy.Plan) {
			opts.Transport = "https"
			opts.HostPort = 7411
			opts.CredentialsDir = "/var/lib/captain/credentials"
			opts.Env = []string{"ANTHROPIC_API_KEY"}
			plan.HostPort = opts.HostPort
			plan.Supervisor = "ssh://captain@host.docker.internal:7422"
			plan.Advertise = "ssh://captain@127.0.0.1:7411/repo.git"
		}, func(config GitAgentDeploymentConfig) {
			Expect(config.HostPort).To(Equal(7411))
			Expect(config.CredentialsDir).To(Equal("/var/lib/captain/credentials"))
			Expect(config.Env).To(Equal([]string{"ANTHROPIC_API_KEY"}))
		}),
		Entry("kubernetes", deploy.TargetKubernetes, "agents", func(opts *GitAgentDeployOptions, plan *deploy.Plan) {
			opts.Transport = "https"
			opts.Domain = "agents.example.com"
			opts.IngressClass = "nginx"
			opts.IngressIssuer = "letsencrypt-prod"
			opts.EnvFromSecret = []string{"model-credentials"}
			opts.CredentialsSecret = "captain-agent-credentials"
			plan.Supervisor = "https://captain.example.com"
			plan.Advertise = "https://worker-01.agents.example.com/git/repo.git"
		}, func(config GitAgentDeploymentConfig) {
			Expect(config.Domain).To(Equal("agents.example.com"))
			Expect(config.IngressClass).To(Equal("nginx"))
			Expect(config.IngressIssuer).To(Equal("letsencrypt-prod"))
			Expect(config.EnvFromSecret).To(Equal([]string{"model-credentials"}))
			Expect(config.CredentialsSecret).To(Equal("captain-agent-credentials"))
		}),
	)

	It("round-trips the edit config through the saved deployment record", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		opts := deployOptions("worker-01")
		opts.Target = "kubernetes"
		opts.Domain = "agents.example.com"
		opts.EnvFromSecret = []string{"model-credentials"}
		plan := deploy.Plan{
			Name: "worker-01", Backend: opts.Backend, Target: deploy.TargetKubernetes,
			Image: opts.Image, Home: opts.Home, ListenPort: opts.ListenPort,
			Supervisor: "https://captain.example.com",
			Advertise:  "https://worker-01.agents.example.com/git/repo.git",
		}

		Expect(recordDeployment(plan, opts, "agents")).To(Succeed())
		saved, found := lookupDeployment(opts.Backend, opts.Name)

		Expect(found).To(BeTrue())
		Expect(saved.Config).NotTo(BeNil())
		Expect(saved.Config.Domain).To(Equal(opts.Domain))
		Expect(saved.Config.EnvFromSecret).To(Equal(opts.EnvFromSecret))
		Expect(saved.Config.SupervisorAddress).To(Equal(plan.Supervisor))
	})

	It("reuses enrollment when replacing a Captain-managed deployment", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		opts := deployOptions("worker-01")
		opts.Replace = true
		plan := deploy.Plan{Name: opts.Name, Backend: opts.Backend, Target: deploy.TargetDocker}
		Expect(recordDeployment(plan, opts, "")).To(Succeed())
		Expect(captainconfig.Update(func(cfg *captainconfig.Config) error {
			backend := cfg.Sandbox.Backends[opts.Backend]
			backend.Options["agents"] = map[string]any{opts.Name: map[string]any{
				"url": "https://worker-01.agents.example.com/git/repo.git",
			}}
			cfg.Sandbox.Backends[opts.Backend] = backend
			return nil
		})).To(Succeed())

		reuse, err := replacementReusesEnrollment(opts)

		Expect(err).NotTo(HaveOccurred())
		Expect(reuse).To(BeTrue())
	})

	It("previews an enrollment-preserving replacement without requiring a live mailbox", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		opts := deployOptions("worker-01")
		opts.Replace = true
		opts.Transport = "ssh"
		opts.HostPort = 7411
		plan := deploy.Plan{
			Name: opts.Name, Backend: opts.Backend, Target: deploy.TargetDocker,
			Image: opts.Image, Home: opts.Home, ListenPort: opts.ListenPort, HostPort: opts.HostPort,
			Supervisor: opts.SupervisorAddress,
			Advertise:  "ssh://captain@127.0.0.1:7411/repo.git",
		}
		Expect(recordDeployment(plan, opts, "")).To(Succeed())
		Expect(captainconfig.Update(func(cfg *captainconfig.Config) error {
			backend := cfg.Sandbox.Backends[opts.Backend]
			backend.Options["agents"] = map[string]any{opts.Name: map[string]any{
				"url":             plan.Advertise,
				"hostFingerprint": "SHA256:agent",
			}}
			cfg.Sandbox.Backends[opts.Backend] = backend
			return nil
		})).To(Succeed())

		result, err := RunGitAgentDeploy(GinkgoT().Context(), opts)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.(GitAgentDeployResult).EnrollmentReused).To(BeFalse(), "dry-run reports mutations, not completion")
	})

	It("refuses to replace enrollment that has no managed state volume", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		opts := deployOptions("worker-01")
		opts.Replace = true
		Expect(captainconfig.Save(captainconfig.Config{Sandbox: captainconfig.SandboxDefaults{
			Backends: map[string]captainconfig.SandboxBackend{opts.Backend: {
				Kind: "git-agent", Options: map[string]any{"agents": map[string]any{
					opts.Name: map[string]any{"url": "ssh://worker-01.example.com/repo.git"},
				}},
			}},
		}})).To(Succeed())

		_, err := replacementReusesEnrollment(opts)

		Expect(err).To(MatchError(ContainSubstring("has no Captain-managed deployment")))
	})

	It("reconstructs every persisted option while pinning the path identity", func() {
		opts := deployOptions("worker-01")
		opts.Target = "docker"
		opts.HostPort = 7411
		opts.CredentialsDir = "/var/lib/captain/credentials"
		opts.ReadOnlyRoot = false
		opts.Wait = false
		plan := deploy.Plan{
			Name: opts.Name, Backend: opts.Backend, Target: deploy.TargetDocker,
			Image: opts.Image, Home: opts.Home, ListenPort: opts.ListenPort,
			HostPort: opts.HostPort, Supervisor: opts.SupervisorAddress,
			Advertise: "ssh://captain@127.0.0.1:7411/repo.git",
		}

		reconstructed := deploymentConfig(plan, opts, "").options("worker-edited", "pool-a")

		Expect(reconstructed.Name).To(Equal("worker-edited"))
		Expect(reconstructed.Backend).To(Equal("pool-a"))
		Expect(reconstructed.Target).To(Equal(opts.Target))
		Expect(reconstructed.HostPort).To(Equal(opts.HostPort))
		Expect(reconstructed.CredentialsDir).To(Equal(opts.CredentialsDir))
		Expect(reconstructed.ReadOnlyRoot).To(BeFalse())
		Expect(reconstructed.Wait).To(BeFalse())
	})

	DescribeTable("refuses to move an edited deployment",
		func(recorded GitAgentDeployment, requested GitAgentDeploymentConfig, message string) {
			Expect(validateDeploymentEdit(recorded, requested)).To(MatchError(ContainSubstring(message)))
		},
		Entry("between runtimes",
			GitAgentDeployment{Target: "docker", Workload: "captain-git-agent-worker-01"},
			GitAgentDeploymentConfig{Target: "kubernetes"},
			"cannot move a deployment from docker to kubernetes"),
		Entry("between namespaces",
			GitAgentDeployment{Target: "kubernetes", Namespace: "agents", Workload: "captain-git-agent-worker-01"},
			GitAgentDeploymentConfig{Target: "kubernetes", Namespace: "other-agents"},
			"cannot move deployment captain-git-agent-worker-01 from namespace \"agents\" to \"other-agents\""),
	)

	It("refuses to change endpoint identity during an enrollment-preserving edit", func() {
		recorded := GitAgentDeployment{
			Target: "kubernetes", Namespace: "agents", Workload: "captain-git-agent-worker-01",
			Config: &GitAgentDeploymentConfig{
				Target: "kubernetes", Namespace: "agents", Transport: "https",
				SupervisorAddress: "https://captain.example.com",
				Advertise:         "https://worker-01.agents.example.com/git/repo.git",
			},
		}
		requested := *recorded.Config
		requested.Advertise = "https://worker-01.other.example.com/git/repo.git"

		Expect(validateDeploymentEdit(recorded, requested)).To(MatchError(ContainSubstring(
			"cannot change the deployment advertised endpoint")))
	})

	It("previews removal of the old workload before replacement", func() {
		opts := deployOptions("worker-01")
		opts.Replace = true
		opts.reuseEnrollment = true
		plan := deploy.Plan{Name: opts.Name, Backend: opts.Backend, Target: deploy.TargetDocker}

		mutations := deployMutations(plan, opts)

		Expect(mutations).NotTo(BeEmpty())
		Expect(mutations[0]).To(ContainSubstring("remove the existing workload"))
		Expect(mutations[0]).To(ContainSubstring(plan.WorkloadName()))
		Expect(mutations).To(ContainElement(ContainSubstring("reuse the existing durable enrollment")))
		Expect(mutations).NotTo(ContainElement(ContainSubstring("mint a durable captain token")))
	})

	It("targets preflight at the saved mailbox transport and kubeconfig context", func() {
		request := httptest.NewRequest(http.MethodGet,
			"/api/captain/sandbox/git-agent/deploy/preflight?backend=pool-a&target=kubernetes&transport=https&kubeContext=lab",
			nil)

		preflight, err := parseGitAgentDeployPreflightRequest(request)

		Expect(err).NotTo(HaveOccurred())
		Expect(preflight.Backend).To(Equal("pool-a"))
		Expect(preflight.Target).To(Equal(deploy.TargetKubernetes))
		Expect(preflight.Transport).To(Equal(transportHTTPS))
		Expect(preflight.KubeContext).To(Equal("lab"))
	})

	It("updates a Docker deployment through the path identity", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		opts := deployOptions("worker-01")
		opts.CredentialsDir = "/var/lib/captain/credentials"
		plan := deploy.Plan{
			Name: opts.Name, Backend: opts.Backend, Target: deploy.TargetDocker,
			Image: opts.Image, Home: opts.Home, ListenPort: opts.ListenPort,
			Supervisor: opts.SupervisorAddress,
		}
		Expect(recordDeployment(plan, opts, "")).To(Succeed())
		saved, found := lookupDeployment(opts.Backend, opts.Name)
		Expect(found).To(BeTrue())
		request := gitAgentDeployRequest{Name: "ignored", GitAgentDeploymentConfig: *saved.Config, DryRun: true}
		request.Image = "registry.example/captain:v2"
		body, err := json.Marshal(request)
		Expect(err).NotTo(HaveOccurred())

		var received GitAgentDeployOptions
		handler := handleGitAgentUpdateWithRunner(func(_ context.Context, update GitAgentDeployOptions) (any, error) {
			received = update
			return GitAgentDeployResult{Agent: update.Name, Image: update.Image, DryRun: update.DryRun}, nil
		})
		mux := http.NewServeMux()
		mux.Handle("PUT /api/captain/sandbox/git-agent/deployments/{name}", handler)
		response := serveHandler(mux, loopbackRequest(http.MethodPut,
			"/api/captain/sandbox/git-agent/deployments/worker-01?backend=git-agent", string(body)))

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(received.Name).To(Equal("worker-01"))
		Expect(received.Replace).To(BeTrue())
		Expect(received.reuseEnrollment).To(BeTrue())
		Expect(received.DryRun).To(BeTrue())
		Expect(received.Image).To(Equal("registry.example/captain:v2"))
		Expect(received.CredentialsDir).To(Equal(opts.CredentialsDir))
	})
})

func serveHandler(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
