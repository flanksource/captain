package gitagent_test

import (
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The protocol's git assumptions (SPEC §1) are re-verified against the
// installed git on every run, so a git upgrade that changes quarantine,
// push-option or materialization behaviour fails loudly here rather than
// silently corrupting a dispatch.
var _ = Describe("verified substrate", func() {
	It("passes the empirical harness against the installed git", func() {
		if _, err := exec.LookPath("git"); err != nil {
			Skip("git not installed")
		}
		if _, err := exec.LookPath("bash"); err != nil {
			Skip("bash not installed")
		}
		script, err := filepath.Abs(filepath.Join("..", "..", "hack", "gitagent_empirical.sh"))
		Expect(err).NotTo(HaveOccurred())
		out, err := exec.Command("bash", script).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "harness output:\n%s", out)
	})
})
