package gitagent_test

import (
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sys/unix"

	"github.com/flanksource/captain/pkg/gitagent"
)

var _ = Describe("agent workspace", func() {
	It("does not inherit unrelated file descriptors", func() {
		repo := GinkgoT().TempDir()
		taskDir := filepath.Join(repo, "captain", "tasks", "t-descriptors")
		Expect(os.MkdirAll(taskDir, 0o755)).To(Succeed())

		unrelated, err := os.Create(filepath.Join(repo, "unrelated"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(unrelated.Close)
		_, err = unix.FcntlInt(unrelated.Fd(), unix.F_SETFD, 0)
		Expect(err).NotTo(HaveOccurred())

		command := fmt.Sprintf("if [ -e /dev/fd/%d ]; then printf inherited; else printf closed; fi", unrelated.Fd())
		Expect(gitagent.LaunchAgent(repo, "t-descriptors", repo, "task.json", command)).To(Succeed())

		Eventually(func() string {
			output, _ := os.ReadFile(filepath.Join(taskDir, "agent.stdout.log"))
			return string(output)
		}).Should(Equal("closed"))
	})
})
