package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

var _ = Describe("git-agent API status", func() {
	It("reports an HTTPS agent with a readable dispatch token as dispatchable", func() {
		tokenPath := filepath.Join(GinkgoT().TempDir(), "w03.token")
		Expect(gitagent.WriteTokenFile(tokenPath, text.NewSensitiveString("dispatch-token"))).To(Succeed())
		backend := captainconfig.SandboxBackend{Kind: "git-agent", Options: map[string]any{
			"agents": map[string]any{
				"w03": map[string]any{
					"url":       "https://w03.agents.lab/git/repo.git",
					"tokenPath": tokenPath,
				},
			},
		}}

		entries := gitAgentRoster(backend)

		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Dispatchable).To(BeTrue())
		Expect(entries[0].DispatchIssue).To(BeEmpty())
		Expect(entries[0].HostFingerprint).To(BeEmpty())
		encoded, err := json.Marshal(entries[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).NotTo(ContainSubstring(tokenPath))
	})

	DescribeTable("reports the transport-specific missing prerequisite",
		func(agent map[string]any, issue string) {
			entries := gitAgentRoster(captainconfig.SandboxBackend{Kind: "git-agent", Options: map[string]any{
				"agents": map[string]any{"worker-01": agent},
			}})

			Expect(entries).To(HaveLen(1))
			Expect(entries[0].Dispatchable).To(BeFalse())
			Expect(entries[0].DispatchIssue).To(Equal(issue))
		},
		Entry("endpoint", map[string]any{}, "missing endpoint"),
		Entry("SSH host key", map[string]any{"url": "ssh://worker-01:7422/repo.git"}, "missing host key"),
		Entry("HTTPS token", map[string]any{"url": "https://worker-01.example.com/git/repo.git"}, "missing dispatch token"),
	)

	It("serves the authenticated whoami contract from the agent", func() {
		var received WhoamiOptions
		handler := agentWhoamiHandler(
			func(r *http.Request) (string, error) {
				if r.Header.Get("Authorization") != "Bearer allowed" {
					return "", fmt.Errorf("invalid credential")
				}
				return supervisorAgentID, nil
			},
			func(options WhoamiOptions) (any, error) {
				received = options
				return map[string]any{"adapters": []any{}}, nil
			},
		)
		request := httptest.NewRequest(http.MethodPost,
			gitagent.AgentWhoamiPath+"?backend=codex-cmux&models=false&limit=2&disabled=true&no-cache=true",
			strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer allowed")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("Content-Type")).To(Equal("application/json"))
		Expect(received).To(Equal(WhoamiOptions{
			Backend: "codex-cmux", Models: false, Limit: 2, IncludeDisabled: true, NoCache: true,
		}))
		Expect(response.Body.String()).To(MatchJSON(`{"adapters":[]}`))
	})

	It("does not probe whoami without the supervisor credential", func() {
		called := false
		handler := agentWhoamiHandler(
			func(*http.Request) (string, error) { return "", fmt.Errorf("invalid credential") },
			func(WhoamiOptions) (any, error) {
				called = true
				return nil, nil
			},
		)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, gitagent.AgentWhoamiPath, nil))

		Expect(response.Code).To(Equal(http.StatusForbidden))
		Expect(called).To(BeFalse())
		Expect(response.Body.String()).NotTo(ContainSubstring("invalid credential"))
	})

	It("proxies an on-demand whoami probe without exposing the dispatch token", func() {
		const dispatchToken = "cptn_dispatch.secret"
		var received *http.Request
		agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received = r.Clone(r.Context())
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"adapters":[{"backend":"codex-agent","type":"cli","provider":"openai","mode":"agent","authenticated":true,"modelCount":1,"models":["gpt-5.6-sol"]}],
				"defaultProvider":"openai","providerDefaults":{},"disabled":{},"axes":{},"runtimes":[]
			}`))
		}))
		DeferCleanup(agent.Close)

		tokenPath := filepath.Join(GinkgoT().TempDir(), "w03.token")
		Expect(gitagent.WriteTokenFile(tokenPath, text.NewSensitiveString(dispatchToken))).To(Succeed())
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		Expect(captainconfig.Save(captainconfig.Config{Sandbox: captainconfig.SandboxDefaults{
			Backends: map[string]captainconfig.SandboxBackend{
				"git-agent": {Kind: "git-agent", Options: map[string]any{
					"agents": map[string]any{"w03": map[string]any{
						"url": agent.URL + "/git/repo.git", "tokenPath": tokenPath,
					}},
				}},
			},
		}})).To(Succeed())

		handler := handleGitAgentWhoamiWithClient(agent.Client())
		response := httptest.NewRecorder()
		request := loopbackRequest(http.MethodPost,
			"/api/captain/sandbox/git-agent/agents/w03/whoami?backend=git-agent", "{}")
		request.SetPathValue("name", "w03")

		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(received).NotTo(BeNil())
		Expect(received.Method).To(Equal(http.MethodPost))
		Expect(received.URL.Path).To(Equal(gitagent.AgentWhoamiPath))
		Expect(received.URL.Query().Get("models")).To(Equal("true"))
		Expect(received.URL.Query().Get("limit")).To(Equal("0"))
		Expect(received.Header.Get("Authorization")).To(Equal("Bearer " + dispatchToken))
		Expect(response.Body.String()).To(ContainSubstring("codex-agent"))
		Expect(response.Body.String()).To(ContainSubstring("gpt-5.6-sol"))
		Expect(response.Body.String()).NotTo(ContainSubstring(dispatchToken))
		Expect(response.Body.String()).NotTo(ContainSubstring(tokenPath))
	})

	It("refuses whoami for an agent without an HTTPS runtime endpoint", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		Expect(captainconfig.Save(captainconfig.Config{Sandbox: captainconfig.SandboxDefaults{
			Backends: map[string]captainconfig.SandboxBackend{
				"git-agent": {Kind: "git-agent", Options: map[string]any{
					"agents": map[string]any{"ssh-worker": map[string]any{
						"url": "ssh://ssh-worker:7422/repo.git", "hostFingerprint": "SHA256:host",
					}},
				}},
			},
		}})).To(Succeed())

		response := httptest.NewRecorder()
		request := loopbackRequest(http.MethodPost,
			"/api/captain/sandbox/git-agent/agents/ssh-worker/whoami?backend=git-agent", "{}")
		request.SetPathValue("name", "ssh-worker")

		handleGitAgentWhoamiWithClient(http.DefaultClient).ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring("does not expose the HTTPS whoami endpoint"))
	})

	It("reloads a mounted route certificate after cert-manager rotates it", func() {
		const host = "w03.agents.example.com"
		first, err := gitagent.EnsureTLSCredential(GinkgoT().TempDir(), []string{host})
		Expect(err).NotTo(HaveOccurred())
		second, err := gitagent.EnsureTLSCredential(GinkgoT().TempDir(), []string{host})
		Expect(err).NotTo(HaveOccurred())

		mounted := GinkgoT().TempDir()
		certPath, keyPath := filepath.Join(mounted, "tls.crt"), filepath.Join(mounted, "tls.key")
		copyTLSFile := func(from, to string) {
			contents, readErr := os.ReadFile(from)
			Expect(readErr).NotTo(HaveOccurred())
			Expect(os.WriteFile(to, contents, 0o600)).To(Succeed())
		}
		copyTLSFile(first.CertPath, certPath)
		copyTLSFile(first.KeyPath, keyPath)

		_, config, err := sidecarTLSConfig(sidecarHTTPSPlan{
			certPath: certPath, keyPath: keyPath,
		}, host)
		Expect(err).NotTo(HaveOccurred())
		presented, err := config.GetCertificate(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(presented.Leaf.SerialNumber).To(Equal(first.Leaf.SerialNumber))

		copyTLSFile(second.CertPath, certPath)
		copyTLSFile(second.KeyPath, keyPath)
		presented, err = config.GetCertificate(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(presented.Leaf.SerialNumber).To(Equal(second.Leaf.SerialNumber))
	})
})
