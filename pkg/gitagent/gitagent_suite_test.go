package gitagent_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
)

// TestMain doubles the test binary as the GIT_SSH_COMMAND client, so push
// tests exercise the production transport with no system ssh installed.
func TestMain(m *testing.M) {
	if os.Getenv("CAPTAIN_TEST_SSH_CLIENT") == "1" {
		os.Exit(gitagent.SSHClientMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestGitAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GitAgent Suite")
}
