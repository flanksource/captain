package agentcreds_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAgentCreds(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AgentCreds Suite")
}
