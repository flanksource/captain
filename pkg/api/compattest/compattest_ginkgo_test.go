package compattest_test

import (
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCompat(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Deprecated approval names")
}

var _ = Describe("deprecated approval identifiers", func() {
	It("still compile for a pre-rename caller", func() {
		output, err := exec.Command("go", "vet", "-tags", "unified_approval_compat", ".").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))
	})
})
