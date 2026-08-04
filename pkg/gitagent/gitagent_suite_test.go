package gitagent_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
)

// TestMain triples the test binary as the GIT_SSH_COMMAND client and as the
// receive-hook shim, so the conformance suite exercises the production
// transport and hook entrypoints with no captain binary installed. The ssh
// branch is checked first: a relay push inside a hook process sets the ssh
// variable explicitly while the hook variable is still inherited.
func TestMain(m *testing.M) {
	if os.Getenv("CAPTAIN_TEST_SSH_CLIENT") == "1" {
		os.Exit(gitagent.SSHClientMain(os.Args[1:]))
	}
	if os.Getenv("CAPTAIN_TEST_HOOK") == "1" {
		os.Exit(gitagent.HookMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestGitAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GitAgent Suite")
}
