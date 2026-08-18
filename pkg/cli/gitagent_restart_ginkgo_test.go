package cli

import (
	"context"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

var _ = Describe("git-agent restart", func() {
	It("starts from complete persisted enrollment while the supervisor is unavailable", func() {
		home := GinkgoT().TempDir()
		captainconfig.SetPathForTesting(filepath.Join(home, ".captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		keysDir := filepath.Join(home, ".captain", "sandbox")
		joinToken := text.NewSensitiveString("cptn_restart.restart-secret")
		joinPath := filepath.Join(home, "join-token")
		storedTokenPath := filepath.Join(keysDir, gitagent.TokenFileName)
		Expect(gitagent.WriteTokenFile(joinPath, joinToken)).To(Succeed())
		Expect(gitagent.WriteTokenFile(storedTokenPath, joinToken)).To(Succeed())
		_, err := gitagent.MintDispatchCredential(filepath.Join(keysDir, gitagent.DispatchCredentialName))
		Expect(err).NotTo(HaveOccurred())
		supervisorTLS, err := gitagent.EnsureTLSCredential(filepath.Join(home, "supervisor"), []string{"127.0.0.1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(captainconfig.Save(captainconfig.Config{Sandbox: captainconfig.SandboxDefaults{
			Backends: map[string]captainconfig.SandboxBackend{
				"git-agent": {Kind: "git-agent", Options: map[string]any{
					"supervisor": map[string]any{
						"url":             "https://127.0.0.1:1",
						"hostFingerprint": supervisorTLS.PublicKeyPin,
						"agent":           "w03",
						"tokenPath":       storedTokenPath,
						"caPath":          supervisorTLS.CertPath,
						"pinnedPubkey":    supervisorTLS.PublicKeyPin,
					},
				}},
			},
		}})).To(Succeed())

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, runErr := RunGitAgentServe(ctx, GitAgentServeOptions{
				Backend: "git-agent", Role: string(gitagent.RoleSidecar), Transport: string(transportHTTPS),
				Listen: "127.0.0.1:0", Advertise: "https://w03.example.com/git/repo.git",
				Supervisor: "https://127.0.0.1:1", HostFingerprint: supervisorTLS.PublicKeyPin,
				TokenFile: joinPath, Root: filepath.Join(home, "repos"),
			})
			done <- runErr
		}()

		Consistently(done, 300*time.Millisecond).ShouldNot(Receive())
		cancel()
		Eventually(done).Should(Receive(BeNil()))
	})
})
